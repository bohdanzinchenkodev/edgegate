package main

import (
	"log"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	"edgegate/internal/config"
	"edgegate/internal/gateway"
)

func main() {
	ctrllog.SetLogger(zap.New())

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{})
	if err != nil {
		log.Fatalf("unable to create manager: %v", err)
	}

	// Register Gateway API types with the scheme so the manager can watch them.
	err = gwv1.Install(mgr.GetScheme())
	if err != nil {
		log.Fatalf("unable to register gateway API types: %v", err)
	}

	// For now, just log what the translator produces.
	// Later this will call egproxy.ApplyConfig.
	applyFn := func(cfg *config.ReverseProxyConfig) {
		for _, l := range cfg.Listeners {
			log.Printf("[apply] listener %s with %d routes", l.Listen, len(l.Routes))
			for _, r := range l.Routes {
				log.Printf("[apply]   host=%s path=%s -> %s", r.Match.Host, r.Match.PathPrefix, r.Upstream)
			}
		}
	}

	// Watch a Gateway named "edgegate" in the "default" namespace.
	// TODO: make this configurable via flags.
	gwName := client.ObjectKey{Namespace: "default", Name: "edgegate"}
	reconciler := gateway.NewGatewayReconciler(mgr.GetClient(), gwName, applyFn)

	err = reconciler.SetupWithManager(mgr)
	if err != nil {
		log.Fatalf("unable to setup controller: %v", err)
	}

	log.Print("starting edgegate-k8s controller")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Fatalf("controller exited: %v", err)
	}
}
