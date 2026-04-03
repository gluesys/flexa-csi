#!/bin/bash

kubectl create -f csi-driver.yml
kubectl create -f controller.yml
kubectl create -f node.yml
#kubectl create -f storage-class.yml

