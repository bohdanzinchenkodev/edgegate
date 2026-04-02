package main

import (
	"log"

	ctrl "sigs.k8s.io/controller-runtime"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	"edgegate/internal/gateway"
	egproxy "edgegate/internal/proxy"
)

func main() {
	ctrllog.SetLogger(zap.New())

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{})
	if err != nil {
		log.Fatalf("unable to create manager: %v", err)
	}

	err = gwv1.Install(mgr.GetScheme())
	if err != nil {
		log.Fatalf("unable to register gateway API types: %v", err)
	}

	reconciler := gateway.NewGatewayReconciler(mgr.GetClient(), egproxy.ApplyConfig)

	err = reconciler.SetupWithManager(mgr)
	if err != nil {
		log.Fatalf("unable to setup controller: %v", err)
	}

	log.Print("starting edgegate-k8s controller")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Fatalf("controller exited: %v", err)
	}
	egproxy.ShutdownAll()
}
