K8S_IMAGE = bohdanzinchenkodev/edgegate-k8s:latest

k8s-image-build:
	docker build -t $(K8S_IMAGE) -f docker/edgegatek8s/Dockerfile .

k8s-image-push: k8s-image-build
	docker push $(K8S_IMAGE)

k8s-deploy-restart: k8s-image-push
	kubectl rollout restart deployment edgegate

k8s-chart-install:
	helm install edgegate ./helm/edgegate

k8s-chart-uninstall:
	helm uninstall edgegate

k8s-apply-samples:
	kubectl apply -f k8s/samples/
