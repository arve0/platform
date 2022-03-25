/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	platformv1 "github.com/arve0/platform/api/v1"
)

// ApplicationReconciler reconciles a Application object
type ApplicationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=platform.arve.dev,resources=applications,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.arve.dev,resources=applications/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.arve.dev,resources=applications/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Application object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *ApplicationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// logr := log.FromContext(ctx)
	logr := log.Log

	logr.Info("reconcile")

	app := &platformv1.Application{}
	err := r.Get(ctx, req.NamespacedName, app)
	if err != nil {
		if errors.IsNotFound(err) {
			logr.Info("deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if app.Status.Phase == platformv1.ApplicationPhaseCreated {
		logr.Info("build")
		return r.build(ctx, app)
	}

	if app.Status.Phase == platformv1.ApplicationPhaseBuilding {
		logr.Info("waiting for build to finish")
		return r.waitForBuild(ctx, app)
	}

	if app.Status.Phase == platformv1.ApplicationPhaseBuildingDone {
		logr.Info("creating deployment")
		return r.createDeployment(ctx, app)
	}

	if app.Status.Phase == platformv1.ApplicationPhaseCreatedDeployment {
		logr.Info("creating service")
		return r.createService(ctx, app)
	}

	return ctrl.Result{}, nil
}

func (r *ApplicationReconciler) build(ctx context.Context, app *platformv1.Application) (ctrl.Result, error) {
	builder := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      getBuilderName(app),
			Namespace: app.Namespace,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:  "builder",
				Image: "gcr.io/kaniko-project/executor",
				Args: []string{
					fmt.Sprintf("--context=%s#refs/heads/%s", app.Spec.Repository.URL, app.Spec.Repository.Revision),
					fmt.Sprintf("--destination=registry.internal/%s:%s", app.Name, app.Spec.Repository.Revision),
					"--insecure-registry=registry.internal",
				},
			}},
		},
	}

	err := ctrl.SetControllerReference(app, &builder, r.Scheme)
	if err != nil {
		return ctrl.Result{}, err
	}

	err = r.Create(ctx, &builder)
	if err != nil {
		return ctrl.Result{}, err
	}

	app.Status.Phase = platformv1.ApplicationPhaseBuilding
	err = r.Status().Update(ctx, app)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func getBuilderName(app *platformv1.Application) string {
	return fmt.Sprintf("builder-%s", app.Name)
}

func (r *ApplicationReconciler) waitForBuild(ctx context.Context, app *platformv1.Application) (ctrl.Result, error) {
	builderName := types.NamespacedName{
		Name:      getBuilderName(app),
		Namespace: app.Namespace,
	}

	builder := corev1.Pod{}
	err := r.Get(ctx, builderName, &builder)
	if err != nil {
		return ctrl.Result{}, err
	}

	if builder.Status.Phase != corev1.PodSucceeded {
		return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
	}

	app.Status.Phase = platformv1.ApplicationPhaseBuildingDone
	err = r.Status().Update(ctx, app)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *ApplicationReconciler) createDeployment(ctx context.Context, app *platformv1.Application) (ctrl.Result, error) {
	deployment := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      app.Name,
			Namespace: app.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": app.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": app.Name,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:            app.Name,
						Image:           fmt.Sprintf("registry.apps.arve.dev/%s:%s", app.Name, app.Spec.Repository.Revision),
						ImagePullPolicy: corev1.PullAlways,
						Env: []corev1.EnvVar{{
							Name:  "PORT",
							Value: "8080",
						}},
						Ports: []corev1.ContainerPort{{
							Name:          "http",
							ContainerPort: 8080,
						}},
					}},
				},
			},
		},
	}

	err := ctrl.SetControllerReference(app, &deployment, r.Scheme)
	if err != nil {
		return ctrl.Result{}, err
	}

	err = r.Create(ctx, &deployment)
	if err != nil {
		return ctrl.Result{}, err
	}

	app.Status.Phase = platformv1.ApplicationPhaseCreatedDeployment
	err = r.Status().Update(ctx, app)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *ApplicationReconciler) createService(ctx context.Context, app *platformv1.Application) (ctrl.Result, error) {
	service := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      app.Name,
			Namespace: app.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"app": app.Name,
			},
			Ports: []corev1.ServicePort{{
				Name:       "http",
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromString("http"),
			}},
		},
	}

	err := ctrl.SetControllerReference(app, &service, r.Scheme)
	if err != nil {
		return ctrl.Result{}, err
	}

	err = r.Create(ctx, &service)
	if err != nil {
		return ctrl.Result{}, err
	}

	app.Status.Phase = platformv1.ApplicationPhaseCreatedService
	err = r.Status().Update(ctx, app)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ApplicationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1.Application{}).
		Complete(r)
}
