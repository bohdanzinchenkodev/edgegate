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

CN = app.example.com
DAYS = 365
TLS_DIR = /tmp/edgegatek8s

tls-gen-cert:
	@mkdir -p $(TLS_DIR)
	openssl req -x509 -newkey rsa:2048 -nodes \
		-keyout $(TLS_DIR)/tls.key -out $(TLS_DIR)/tls.crt \
		-days $(DAYS) -subj "/CN=$(CN)"

k8s-tls-secret: tls-gen-cert
	kubectl create secret tls app-tls \
		--cert=$(TLS_DIR)/tls.crt --key=$(TLS_DIR)/tls.key \
		--dry-run=client -o yaml | kubectl apply -f -

k8s-apply-samples:
	kubectl apply -f k8s/samples/

k8s-test-http:
	@EXTERNAL_IP=$$(kubectl get svc edgegate -o jsonpath='{.status.loadBalancer.ingress[0].ip}'); \
	curl http://$$EXTERNAL_IP/get

bench-start:
	./bench/start-proxy.sh $(VARIANT)

bench-start-tls:
	./bench/start-proxy.sh tls

bench-start-ratelimit:
	./bench/start-proxy.sh ratelimit

bench-attack:
	./bench/attack-proxy.sh

bench-attack-tls:
	./bench/attack-proxy.sh --tls

bench-swap:
	./bench/swap-config.sh $(VARIANT)

bench-baseline:
	./bench/upstream-direct.sh

k8s-e2e:
	./e2e/run.sh

k8s-e2e-debug:
	./e2e/run.sh --no-teardown

k8s-e2e-teardown:
	./e2e/teardown.sh

k8s-test-https:
	@EXTERNAL_IP=$$(kubectl get svc edgegate -o jsonpath='{.status.loadBalancer.ingress[0].ip}'); \
	curl --resolve $(CN):443:$$EXTERNAL_IP https://$(CN)/get -k
