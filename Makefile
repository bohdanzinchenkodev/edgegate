K8S_IMAGE = bohdanzinchenkodev/edgegate-k8s:latest
CHART_REPO = /tmp/edgegate-chart
CHART_REPO_GIT = https://github.com/bohdanzinchenkodev/edgegate-chart.git
CHART_URL = https://bohdanzinchenkodev.github.io/edgegate-chart

k8s-image-build:
	docker build -t $(K8S_IMAGE) -f k8s/Dockerfile .

k8s-image-push: k8s-image-build
	docker push $(K8S_IMAGE)

k8s-deploy-restart: k8s-image-push
	kubectl rollout restart deployment edgegate

k8s-chart-install:
	helm install edgegate ./k8s/helm

k8s-chart-install-repo:
	helm repo add edgegate $(CHART_URL) && helm repo update
	helm install edgegate edgegate/edgegate

k8s-chart-uninstall:
	helm uninstall edgegate

k8s-chart-repo:
	@if [ -d $(CHART_REPO)/.git ]; then \
		cd $(CHART_REPO) && git pull; \
	else \
		git clone $(CHART_REPO_GIT) $(CHART_REPO); \
	fi

k8s-chart-package: k8s-chart-repo
	helm package ./k8s/helm -d $(CHART_REPO)

k8s-chart-index:
	helm repo index $(CHART_REPO) --url $(CHART_URL)

k8s-chart-publish: k8s-chart-package k8s-chart-index
	cd $(CHART_REPO) && git add . && git commit -m "publish edgegate chart" && git push origin main

CN = edgegate.local
DAYS = 365

tls-gen-cert:
	openssl req -x509 -newkey rsa:2048 -nodes \
		-keyout tls.key -out tls.crt \
		-days $(DAYS) -subj "/CN=$(CN)"

k8s-apply-samples:
	kubectl apply -f k8s/samples/
