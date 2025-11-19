#!/bin/sh

containerd &

while [ ! -S /run/containerd-sock/containerd.sock ]; do
  echo "Waiting for containerd socket..."
  sleep 1
done

chown ${USER_ID}:${GROUP_ID} /run/containerd-sock/containerd.sock

touch /run/containerd-sock/ready

wait

rm /run/containerd-sock/ready
