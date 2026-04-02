K8S_IMAGE = bohdanzinchenkodev/edgegate-k8s:latest

k8s-build:
	docker build -t $(K8S_IMAGE) -f docker/edgegatek8s/Dockerfile .

k8s-push: k8s-build
	docker push $(K8S_IMAGE)

k8s-deploy: k8s-push
	kubectl rollout restart deployment edgegate
