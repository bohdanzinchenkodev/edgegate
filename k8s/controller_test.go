package k8s

import (
	"context"
	"io"
	"log"
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	"edgegate/internal/config"
)

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = gwv1.Install(s)
	return s
}

func TestReconcile_MatchingGatewayAppliesConfig(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)

	gc := &gwv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "edgegate"},
		Spec:       gwv1.GatewayClassSpec{ControllerName: controllerName},
	}
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "my-gw", Namespace: "default"},
		Spec: gwv1.GatewaySpec{
			GatewayClassName: "edgegate",
			Listeners:        []gwv1.Listener{{Name: "http", Port: 8080}},
		},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "edgegate", Namespace: "default"},
	}

	c := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(gc, gw, svc).
		WithStatusSubresource(&gwv1.Gateway{}).
		Build()

	var applied *config.ReverseProxyConfig
	applyFn := func(cfg *config.ReverseProxyConfig) { applied = cfg }

	gr := NewGatewayReconciler(c, applyFn, client.ObjectKey{Namespace: "default", Name: "edgegate"})
	_, err := gr.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKey{Namespace: "default", Name: "my-gw"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if applied == nil {
		t.Fatalf("expected config to be applied")
	}
	if len(applied.Listeners) != 1 {
		t.Fatalf("expected 1 listener, got %d", len(applied.Listeners))
	}
	if applied.Listeners[0].Listen != ":8080" {
		t.Fatalf("expected listen :8080, got %s", applied.Listeners[0].Listen)
	}
}

func TestReconcile_NonMatchingControllerNameSkips(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)

	gc := &gwv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: "other-class"},
		Spec:       gwv1.GatewayClassSpec{ControllerName: "other-controller"},
	}
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "my-gw", Namespace: "default"},
		Spec: gwv1.GatewaySpec{
			GatewayClassName: "other-class",
			Listeners:        []gwv1.Listener{{Name: "http", Port: 8080}},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(gc, gw).
		Build()

	var applied *config.ReverseProxyConfig
	applyFn := func(cfg *config.ReverseProxyConfig) { applied = cfg }

	gr := NewGatewayReconciler(c, applyFn, client.ObjectKey{Namespace: "default", Name: "edgegate"})
	_, err := gr.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKey{Namespace: "default", Name: "my-gw"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if applied != nil {
		t.Fatalf("expected config to NOT be applied for non-matching controller")
	}
}

func TestReconcile_GatewayNotFoundReturnsNoError(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(testScheme()).
		Build()

	applyFn := func(cfg *config.ReverseProxyConfig) {
		t.Fatalf("applyConfig should not be called for missing gateway")
	}

	gr := NewGatewayReconciler(c, applyFn, client.ObjectKey{Namespace: "default", Name: "edgegate"})
	_, err := gr.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKey{Namespace: "default", Name: "missing-gw"},
	})
	if err != nil {
		t.Fatalf("expected no error for missing gateway, got %v", err)
	}
}

func TestFetchTLSSecrets_FetchesReferencedSecrets(t *testing.T) {
	s := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "app-tls", Namespace: "default"},
		Data: map[string][]byte{
			"tls.crt": []byte("cert-data"),
			"tls.key": []byte("key-data"),
		},
	}

	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "my-gw", Namespace: "default"},
		Spec: gwv1.GatewaySpec{
			Listeners: []gwv1.Listener{{
				Name: "http",
				Port: 8080,
				TLS: &gwv1.ListenerTLSConfig{
					CertificateRefs: []gwv1.SecretObjectReference{
						{Name: "app-tls", Namespace: namespacePtr("default")},
					},
				},
			}},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(&s).
		Build()

	gr := NewGatewayReconciler(c, nil, client.ObjectKey{})
	secrets := gr.fetchTLSSecrets(context.Background(), gw)

	certData, ok := secrets["default/app-tls"]
	if !ok {
		t.Fatal("expected secret default/app-tls not found in returned map")
	}
	if string(certData.CertData) != "cert-data" {
		t.Fatal("expected cert-data not found in returned secret")
	}
	if string(certData.KeyData) != "key-data" {
		t.Fatal("expected key-data not found in returned secret")
	}

}

func TestFetchTLSSecrets_SkipsListenersWithoutTLS(t *testing.T) {
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "my-gw", Namespace: "default"},
		Spec: gwv1.GatewaySpec{
			Listeners: []gwv1.Listener{{Name: "http", Port: 8080}},
		},
	}

	c := fake.NewClientBuilder().WithScheme(testScheme()).Build()
	gr := NewGatewayReconciler(c, nil, client.ObjectKey{})

	secrets := gr.fetchTLSSecrets(context.Background(), gw)

	if len(secrets) != 0 {
		t.Fatalf("expected no secrets for HTTP listener, got %d", len(secrets))
	}
}

func TestFetchTLSSecrets_MissingSecretContinues(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)

	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "my-gw", Namespace: "default"},
		Spec: gwv1.GatewaySpec{
			Listeners: []gwv1.Listener{
				{
					Name: "https",
					Port: 443,
					TLS: &gwv1.ListenerTLSConfig{
						CertificateRefs: []gwv1.SecretObjectReference{
							{Name: "does-not-exist"},
						},
					},
				},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(testScheme()).Build()
	gr := NewGatewayReconciler(c, nil, client.ObjectKey{})

	secrets := gr.fetchTLSSecrets(context.Background(), gw)

	if len(secrets) != 0 {
		t.Fatalf("expected empty map when secret is missing, got %d", len(secrets))
	}
}

func TestSyncServicePorts_UpdatesWhenDifferent(t *testing.T) {
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "edgegate", Namespace: "default"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Name: "old", Port: 80, Protocol: corev1.ProtocolTCP},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(svc).
		Build()

	gr := NewGatewayReconciler(c, nil, client.ObjectKey{Namespace: "default", Name: "edgegate"})
	listeners := []gwv1.Listener{
		{Name: "http", Port: 8080},
		{Name: "https", Port: 443},
	}

	err := gr.syncServicePorts(context.Background(), listeners)
	if err != nil {
		t.Fatalf("syncServicePorts failed: %v", err)
	}

	var updated corev1.Service
	_ = c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "edgegate"}, &updated)

	if len(updated.Spec.Ports) != 2 {
		t.Fatalf("expected 2 ports, got %d", len(updated.Spec.Ports))
	}
	if updated.Spec.Ports[0].Port != 8080 {
		t.Fatalf("expected first port 8080, got %d", updated.Spec.Ports[0].Port)
	}
	if updated.Spec.Ports[1].Port != 443 {
		t.Fatalf("expected second port 443, got %d", updated.Spec.Ports[1].Port)
	}
}

func TestSyncServicePorts_SkipsUpdateWhenEqual(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "edgegate", Namespace: "default"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 8080, Protocol: corev1.ProtocolTCP},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(svc).
		Build()

	gr := NewGatewayReconciler(c, nil, client.ObjectKey{Namespace: "default", Name: "edgegate"})
	listeners := []gwv1.Listener{{Name: "http", Port: 8080}}

	err := gr.syncServicePorts(context.Background(), listeners)
	if err != nil {
		t.Fatalf("syncServicePorts failed: %v", err)
	}
}

func TestHttpRouteToGateway_MapsToGatewayRequests(t *testing.T) {
	hr := &gwv1.HTTPRoute{
		Spec: gwv1.HTTPRouteSpec{
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{
					{Name: "gw-1"},
					{Name: "gw-2", Namespace: namespacePtr("other-ns")},
				},
			},
		},
	}
	hr.Namespace = "default"

	requests := httpRouteToGateway(context.Background(), hr)

	if len(requests) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(requests))
	}
	if requests[0].NamespacedName.Name != "gw-1" || requests[0].NamespacedName.Namespace != "default" {
		t.Fatalf("unexpected first request: %v", requests[0])
	}
	if requests[1].NamespacedName.Name != "gw-2" || requests[1].NamespacedName.Namespace != "other-ns" {
		t.Fatalf("unexpected second request: %v", requests[1])
	}
}

func TestHttpRouteToGateway_NonHTTPRouteReturnsNil(t *testing.T) {
	pod := &corev1.Pod{}

	requests := httpRouteToGateway(context.Background(), pod)

	if requests != nil {
		t.Fatalf("expected nil for non-HTTPRoute object, got %v", requests)
	}
}
