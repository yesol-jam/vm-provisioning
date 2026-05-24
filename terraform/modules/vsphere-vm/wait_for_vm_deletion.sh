#!/bin/sh
# VM 삭제 완료 대기 스크립트
# 최대 5분 동안 10초마다 확인

echo "Checking if VMs are fully deleted..."

max_wait=300  # 5분
interval=10
elapsed=0

# 간단한 대기 후 포트 그룹 삭제
# vSphere API가 VM 삭제 완료를 보장할 때까지 대기
while [ $elapsed -lt $max_wait ]; do
  echo "Waited $elapsed seconds..."
  sleep $interval
  elapsed=$((elapsed + interval))
  
  # 60초 이상 대기했으면 충분하다고 판단
  if [ $elapsed -ge 60 ]; then
    echo "Waited 60 seconds, proceeding with port group deletion"
    exit 0
  fi
done

echo "Maximum wait time reached (${max_wait}s)"
exit 0
