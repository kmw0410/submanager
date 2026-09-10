# Docker named volume을 호스트 마운트로 이전하기

기본 `docker-compose.yml`은 기존처럼 `submanager-data` named volume을 `/data`에 연결합니다. 이 문서는 그 데이터를 원하는 호스트 폴더로 한 번만 옮기는 절차입니다.

애플리케이션은 호스트 폴더 이름을 사용하거나 감지하지 않습니다. 컨테이너 내부의 `DB_PATH`만 사용하므로, 아래 예시의 `./submanager-data`는 `./data`, `./backup/submanager`, `/srv/submanager-data` 등 원하는 호스트 경로로 바꿔도 됩니다. 해당 경로가 컨테이너의 `/data`로 마운트되어야 합니다.

## 이전 절차

1. 현재 컨테이너를 중지합니다. `down`은 named volume을 삭제하지 않습니다.

   ```bash
   docker compose down
   ```

2. 호스트의 대상 폴더를 만들고, 컨테이너의 `submanager` 사용자(UID `100`, GID `101`)가 쓸 수 있도록 권한을 부여합니다. 이 단계가 없으면 일반적인 Linux bind mount에서 컨테이너가 DB 파일을 만들지 못할 수 있습니다.

   ```bash
   mkdir -p ./submanager-data
   sudo chown 100:101 ./submanager-data
   ```

3. `docker-compose.yml`에서 서비스의 `environment`와 `volumes`를 아래처럼 **일시적으로** 바꿉니다. 첫 번째 경로만 원하는 호스트 폴더로 변경하면 됩니다.

   ```yaml
   environment:
     DB_PATH: /data/submanager.db
     TZ: ${TZ:-Asia/Seoul}
     MIGRATE_DATA: "true"
     MIGRATE_DATA_SOURCE: /migration-source/submanager.db
   volumes:
     - ./submanager-data:/data
     - submanager-data:/migration-source:ro
   ```

   기존 named volume은 `/migration-source`에 읽기 전용으로 마운트되므로, 이전 중 원본이 변경되지 않습니다.

4. 컨테이너를 시작하고 로그를 확인합니다.

   ```bash
   docker compose up -d
   docker compose logs submanager
   ```

   성공하면 대상 DB의 `app_metadata`에 `docker_volume_to_host_mount_v1=1`이 기록됩니다. `MIGRATE_DATA`가 계속 `true`인 상태에서 재시작해도 다시 복사하지 않고 다음 로그를 남깁니다.

   ```text
   migrate is already complete. skipping environment
   ```

   오류로 끝난 경우 완료 상태는 기록되지 않습니다. 원인을 해결한 뒤 같은 설정으로 다시 시작하면 이전을 재시도합니다.

5. 이전을 확인한 뒤 `MIGRATE_DATA`와 `MIGRATE_DATA_SOURCE`를 제거하고, `volumes`는 호스트 마운트만 남깁니다.

   ```yaml
   volumes:
     - ./submanager-data:/data
   ```

   더 이상 원본이 필요 없을 때만 `submanager-data` named volume과 Compose 파일 하단의 해당 volume 선언을 삭제하세요.

## 주의 사항

- 완료 표시가 없는 대상 DB는 원본 named volume의 복사본으로 교체됩니다. 비어 있거나 교체해도 되는 호스트 폴더에서만 `MIGRATE_DATA=true`를 사용하세요.
- 대상 폴더 이름은 이전 완료 여부와 무관합니다. 완료 상태는 대상 데이터베이스 안에 저장됩니다.
- 컨테이너 내부 `/data` 외의 경로를 사용하려면 `DB_PATH`와 호스트 마운트 대상을 함께 일치시켜야 합니다.
