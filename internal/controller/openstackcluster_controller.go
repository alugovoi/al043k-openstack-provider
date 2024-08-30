/*
Copyright 2024.

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

package controller

import (
	"context"
    "strings"
    "strconv"


	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
    apierrors "k8s.io/apimachinery/pkg/api/errors"
    clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
    "sigs.k8s.io/cluster-api/util/patch"
    "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
    "sigs.k8s.io/controller-runtime/pkg/reconcile"


    "github.com/gophercloud/utils/openstack/clientconfig"
    "github.com/gophercloud/gophercloud/openstack"
    "github.com/gophercloud/gophercloud"
    "github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/loadbalancers"



	infrastructurev1alpha1 "openstack-infrastructure-provider/api/v1alpha1"
)

// OpenstackClusterReconciler reconciles a OpenstackCluster object
type OpenstackClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=openstackclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=openstackclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=openstackclusters/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the OpenstackCluster object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.0/pkg/reconcile
func (r *OpenstackClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {

	// TODO(user): your logic here
    log := ctrl.LoggerFrom(ctx)
    // TODO(user): your logic here
    log.Info("Openstack infrastructure reconcile loop \n")


    OpenstackCluster := &infrastructurev1alpha1.OpenstackCluster{}
    if err := r.Client.Get(ctx, req.NamespacedName, OpenstackCluster); err != nil {
        if apierrors.IsNotFound(err) {
            return ctrl.Result{}, nil
        }
        return ctrl.Result{}, err
    }
    patchHelper, err := patch.NewHelper(OpenstackCluster, r.Client)
    if err != nil {
        return ctrl.Result{}, err
    }

    // Always patch the openStackCluster when exiting this function so we can persist any OpenStackCluster changes.
    defer func() {
        log.Info("updating the k8s object")
        if err := patchHelper.Patch(ctx, OpenstackCluster); err != nil {
            log.Info("failed to patch infra openstack cluster")
        }
        log.Info("successfully updated the k8s object")
    }()

    log.Info("Adding finalizer")
    // If the OpenStackCluster doesn't have our finalizer, add it.
    if controllerutil.AddFinalizer(OpenstackCluster, infrastructurev1alpha1.ClusterFinalizer) {
        // Register the finalizer immediately to avoid orphaning OpenStack resources on delete
        return reconcile.Result{}, nil
    }
    log.Info("Added finalizer")



    opts := new(clientconfig.ClientOpts)
    opts.Cloud = "telstra-kildalab-k0s-alugovoi"
    //   opts.HTTPClient = http_client

    provider, err := clientconfig.AuthenticatedClient(opts)
    if err != nil {
        panic(err)
    }
    // Handle deleted clusters
    if !OpenstackCluster.DeletionTimestamp.IsZero() {
        log.Info("reconcile deletion of clusters")
        return r.reconcileDelete(ctx, OpenstackCluster)
    }

    cluster_name := strings.Split(req.String(), "/")[1]

    lbClient, err := openstack.NewLoadBalancerV2(provider, gophercloud.EndpointOpts{})
    _ = lbClient

    //Lets check if the infrastructure cluster exists. We will use LB_cluster_name format in the lb's name to filter
    listOpts := loadbalancers.ListOpts{
        Name: "LB_" + cluster_name,
    }

    allPages, err := loadbalancers.List(lbClient, listOpts).AllPages()
    if err != nil {
        panic(err)
    }
    allLoadbalancers, err := loadbalancers.ExtractLoadBalancers(allPages)
    if err != nil {
        panic(err)
    }

    var lb loadbalancers.LoadBalancer

    if len(allLoadbalancers) != 0 {
        log.Info("There is existing infra LB for the cluster")
        log.Info("There is " + strconv.Itoa(len(allLoadbalancers)) + " lbs available")
        lb = allLoadbalancers[0]
        log.Info("The LB ID is " + lb.ID)
    } else {
        createOpts := loadbalancers.CreateOpts{
            Name:         "LB_" + cluster_name,
            //VipNetworkID: "0b52d445-d7a8-428e-832e-aa293860d0cb",
            VipNetworkID: OpenstackCluster.Spec.LBOpenstackNetwork,
            Provider: "amphorav2",
            Tags:     []string{"test", "stage"},
        }

        log.Info("There is no infrastructure LB. Creating LB for cluster " + cluster_name)
        log.Info("LB network from the spec is " + OpenstackCluster.Spec.LBOpenstackNetwork)
        created_lb, err := loadbalancers.Create(lbClient, createOpts).Extract()
        if err != nil {
            panic(err)
        }
        lb = *created_lb
        log.Info("The LB ID is " + lb.ID)
    }

    //fmt.Printf("The LB for cluster:  %s\t is  %s\n", cluster_name, lb.Name)
    log.Info("LB vip is: " + lb.VipAddress)

    //log.Info("OpenstackCluster name is " + OpenstackCluster.Name)

    OpenstackCluster.Spec.ControlPlaneEndpoint = &clusterv1.APIEndpoint{
        Host: lb.VipAddress,
        Port: 6443,
    }
    //OpenstackCluster.Spec.ControlPlaneEndpoint = ControlPlaneEndpoint

    labels := map[string]string{
        "lb_uuid": lb.ID,
    }
    OpenstackCluster.SetLabels(labels)

    log.Info("Reconcile loop is finished")

    log.Info("Marking openstack cluster as ready")
    OpenstackCluster.Status.Ready = true

    return ctrl.Result{}, nil
}

func (r *OpenstackClusterReconciler) reconcileDelete(ctx context.Context, OpenstackCluster *infrastructurev1alpha1.OpenstackCluster) (ctrl.Result, error) {
    opts := new(clientconfig.ClientOpts)
    opts.Cloud = "telstra-kildalab-k0s-alugovoi"
    //   opts.HTTPClient = http_client

    provider, err := clientconfig.AuthenticatedClient(opts)
    if err != nil {
        panic(err)
    }
    lbClient, err := openstack.NewLoadBalancerV2(provider, gophercloud.EndpointOpts{})
    _ = lbClient
    deleteOpts := loadbalancers.DeleteOpts{
        Cascade: true,
    }
    loadbalancers.Delete(lbClient, OpenstackCluster.Labels["lb_uuid"], deleteOpts)
    controllerutil.RemoveFinalizer(OpenstackCluster, infrastructurev1alpha1.ClusterFinalizer)

    return ctrl.Result{}, err

}





// SetupWithManager sets up the controller with the Manager.
func (r *OpenstackClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1alpha1.OpenstackCluster{}).
		Complete(r)
}
