package k8s

import (
	"context"
	"log"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	"edgegate/internal/config"
)

const controllerName = "edgegate.io/gateway-controller"

type ApplyConfigFunc func(cfg *config.ReverseProxyConfig)

type GatewayReconciler struct {
	client      client.Client
	applyConfig ApplyConfigFunc
	serviceName client.ObjectKey
}

func NewGatewayReconciler(c client.Client, applyFn ApplyConfigFunc, serviceName client.ObjectKey) *GatewayReconciler {
	return &GatewayReconciler{
		client:      c,
		applyConfig: applyFn,
		serviceName: serviceName,
	}
}

func (gr *GatewayReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var gw gwv1.Gateway
	if err := gr.client.Get(ctx, req.NamespacedName, &gw); err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{}, err
		}

		hasManaged, listErr := gr.hasManagedGateways(ctx)
		if listErr != nil {
			return reconcile.Result{}, listErr
		}

		if !hasManaged {
			log.Printf("[gateway] no managed Gateways remain, applying empty config")
			gr.applyConfig(&config.ReverseProxyConfig{})
			if err := gr.syncServicePorts(ctx, nil); err != nil {
				log.Printf("[gateway] failed to clear service ports: %v", err)
				return reconcile.Result{}, err
			}
		}

		return reconcile.Result{}, nil
	}

	// Fetch the GatewayClass to verify this Gateway belongs to us.
	var gc gwv1.GatewayClass
	if err := gr.client.Get(ctx, client.ObjectKey{Name: string(gw.Spec.GatewayClassName)}, &gc); err != nil {
		return reconcile.Result{}, err
	}
	if string(gc.Spec.ControllerName) != controllerName {
		return reconcile.Result{}, nil
	}

	var httpRouteList gwv1.HTTPRouteList
	if err := gr.client.List(ctx, &httpRouteList); err != nil {
		return reconcile.Result{}, err
	}
	var matchingRoutes []*gwv1.HTTPRoute
	for i := range httpRouteList.Items {
		hr := &httpRouteList.Items[i]
		for _, ref := range hr.Spec.ParentRefs {
			if string(ref.Name) == gw.Name {
				matchingRoutes = append(matchingRoutes, hr)
				break
			}
		}
	}

	tlsSecrets := gr.fetchTLSSecrets(ctx, &gw)
	cfg := Translate(&gw, matchingRoutes, tlsSecrets)
	log.Printf("[gateway] reconciled: %d listeners, %d routes",
		len(cfg.Listeners), countRoutes(cfg))
	gr.applyConfig(cfg)

	if err := gr.syncServicePorts(ctx, gw.Spec.Listeners); err != nil {
		log.Printf("[gateway] failed to sync service ports: %v", err)
	}

	gr.setGatewayStatus(ctx, &gw)

	return reconcile.Result{}, nil
}

func (gr *GatewayReconciler) setGatewayStatus(ctx context.Context, gw *gwv1.Gateway) {
	now := metav1.Now()
	gw.Status.Conditions = []metav1.Condition{
		{
			Type:               string(gwv1.GatewayConditionAccepted),
			Status:             metav1.ConditionTrue,
			ObservedGeneration: gw.Generation,
			LastTransitionTime: now,
			Reason:             string(gwv1.GatewayReasonAccepted),
		},
		{
			Type:               string(gwv1.GatewayConditionProgrammed),
			Status:             metav1.ConditionTrue,
			ObservedGeneration: gw.Generation,
			LastTransitionTime: now,
			Reason:             string(gwv1.GatewayReasonProgrammed),
		},
	}

	if err := gr.client.Status().Update(ctx, gw); err != nil {
		log.Printf("[gateway] failed to update gateway status: %v", err)
	}
}

func (gr *GatewayReconciler) fetchTLSSecrets(ctx context.Context, gw *gwv1.Gateway) map[string]TLSSecret {
	secrets := make(map[string]TLSSecret)
	for _, listener := range gw.Spec.Listeners {
		if listener.TLS == nil {
			continue
		}
		for _, ref := range listener.TLS.CertificateRefs {
			ns := gw.Namespace
			if ref.Namespace != nil {
				ns = string(*ref.Namespace)
			}
			key := ns + "/" + string(ref.Name)
			if _, ok := secrets[key]; ok {
				continue
			}
			var secret corev1.Secret
			if err := gr.client.Get(ctx, client.ObjectKey{Namespace: ns, Name: string(ref.Name)}, &secret); err != nil {
				log.Printf("[gateway] failed to fetch TLS secret %s: %v", key, err)
				continue
			}
			secrets[key] = TLSSecret{
				CertData: secret.Data["tls.crt"],
				KeyData:  secret.Data["tls.key"],
			}
		}
	}
	return secrets
}

func (gr *GatewayReconciler) syncServicePorts(ctx context.Context, listeners []gwv1.Listener) error {
	var svc corev1.Service
	if err := gr.client.Get(ctx, gr.serviceName, &svc); err != nil {
		return err
	}

	desired := make([]corev1.ServicePort, len(listeners))
	for i, l := range listeners {
		port := int32(l.Port)
		desired[i] = corev1.ServicePort{
			Name:       string(l.Name),
			Port:       port,
			TargetPort: intstr.FromInt32(port),
			Protocol:   corev1.ProtocolTCP,
		}
	}

	if portsEqual(svc.Spec.Ports, desired) {
		return nil
	}

	svc.Spec.Ports = desired
	log.Printf("[gateway] updating service ports: %v", desired)
	return gr.client.Update(ctx, &svc)
}

func portsEqual(a, b []corev1.ServicePort) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Port != b[i].Port || a[i].Name != b[i].Name {
			return false
		}
	}
	return true
}

func httpRouteToGateway(ctx context.Context, obj client.Object) []reconcile.Request {
	hr, ok := obj.(*gwv1.HTTPRoute)
	if !ok {
		return nil
	}
	var requests []reconcile.Request
	for _, ref := range hr.Spec.ParentRefs {
		ns := hr.Namespace
		if ref.Namespace != nil {
			ns = string(*ref.Namespace)
		}
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKey{Namespace: ns, Name: string(ref.Name)},
		})
	}
	return requests
}

func (gr *GatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gwv1.Gateway{}).
		Watches(&gwv1.HTTPRoute{}, handler.EnqueueRequestsFromMapFunc(httpRouteToGateway)).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(gr.secretToGateway)).
		Complete(gr)
}

func (gr *GatewayReconciler) hasManagedGateways(ctx context.Context) (bool, error) {
	var gateways gwv1.GatewayList
	if err := gr.client.List(ctx, &gateways); err != nil {
		return false, err
	}

	for i := range gateways.Items {
		gw := &gateways.Items[i]

		var gc gwv1.GatewayClass
		err := gr.client.Get(ctx, client.ObjectKey{Name: string(gw.Spec.GatewayClassName)}, &gc)
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return false, err
		}
		if string(gc.Spec.ControllerName) == controllerName {
			return true, nil
		}
	}

	return false, nil
}

func (gr *GatewayReconciler) secretToGateway(ctx context.Context, obj client.Object) []reconcile.Request {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}

	var gateways gwv1.GatewayList
	if err := gr.client.List(ctx, &gateways); err != nil {
		log.Printf("[gateway] failed to list Gateways for secret mapping: %v", err)
		return nil
	}

	requests := make([]reconcile.Request, 0)
	seen := make(map[client.ObjectKey]struct{})

	for i := range gateways.Items {
		gw := &gateways.Items[i]
		for _, listener := range gw.Spec.Listeners {
			if listener.TLS == nil {
				continue
			}
			for _, ref := range listener.TLS.CertificateRefs {
				ns := gw.Namespace
				if ref.Namespace != nil {
					ns = string(*ref.Namespace)
				}
				if ns != secret.Namespace || string(ref.Name) != secret.Name {
					continue
				}

				key := client.ObjectKey{Namespace: gw.Namespace, Name: gw.Name}
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				requests = append(requests, reconcile.Request{NamespacedName: key})
			}
		}
	}

	return requests
}

func countRoutes(cfg *config.ReverseProxyConfig) int {
	n := 0
	for _, l := range cfg.Listeners {
		n += len(l.Routes)
	}
	return n
}
