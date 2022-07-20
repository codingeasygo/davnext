#!/bin/sh
set -xe
docker build -t davnext:v1.0.0 .
if [ "$1" != "" ];then
    docker tag davnext:v1.0.0 $1/davnext:v1.0.0
    docker push $1/davnext:v1.0.0
fi
