REGISTRY := $(or $(REGISTRY), gcr.io/playground-237408)
TAG := $(or $(TAG), latest)

docker: controller-docker generator-docker worker-docker

docker-push: docker
	docker push ${REGISTRY}/batched-jobs/controller:${TAG}
	docker push ${REGISTRY}/batched-jobs/generator:${TAG}
	docker push ${REGISTRY}/batched-jobs/worker:${TAG}

controller-docker: controller-build
	cd cmd/controller && docker build -t ${REGISTRY}/batched-jobs/controller:${TAG} .

controller-build:
	go build -o ./cmd/controller/controller ./cmd/controller

generator-docker: generator-build
	cd cmd/generator && docker build -t ${REGISTRY}/batched-jobs/generator:${TAG} .

generator-build:
	go build -o ./cmd/generator/generator ./cmd/generator

worker-docker: worker-build
	cd cmd/worker && docker build -t ${REGISTRY}/batched-jobs/worker:${TAG} .

worker-build:
	go build -o ./cmd/worker/worker ./cmd/worker

deploy: docker-push
	kubectl apply -f ./deployment/serviceaccount.yml
	kubectl apply -f ./deployment/redis.yml
	cat ./deployment/controller.yml | sed "s|{{REGISTRY}}|${REGISTRY}|" | sed "s|{{TAG}}|${TAG}|" | kubectl apply -f -

run:
	kubectl run -it --rm --image=${REGISTRY}/batched-jobs/generator:${TAG} --replicas=1 --generator=run-pod/v1 --env="REDIS_ADDR=redis:6379" --restart=Never generator

clean:
	cat ./deployment/controller.yml | sed "s|{{REGISTRY}}|${REGISTRY}|" | sed "s|{{TAG}}|${TAG}|" | kubectl delete -f -
	kubectl delete -f ./deployment/redis.yml
	kubectl delete -f ./deployment/serviceaccount.yml