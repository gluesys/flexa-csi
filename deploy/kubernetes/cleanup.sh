#!/bin/bash

kubectl delete -f storage-class.yml
kubectl delete -f controller.yml
kubectl delete -f node.yml
kubectl delete -f csi-driver.yml
