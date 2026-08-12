# Submanager

한국 사용자를 위한 가벼운 self-hosted 구독 관리 대시보드입니다. Go, SQLite, 서버 렌더링 HTML과 최소한의 Vanilla JavaScript로 구성됩니다.

## Docker로 실행

```bash
docker compose up -d
```

GitHub Container Registry에 게시된 이미지는 다음과 같이 받을 수 있습니다.

```bash
docker pull ghcr.io/kmw0410/submanager:latest
```

브라우저에서 `http://localhost:8080`에 접속합니다. 포트를 바꾸려면 `PORT=3000 docker compose up -d`처럼 실행하세요. 데이터는 `submanager-data` Docker volume의 `/data/submanager.db`에 저장됩니다.

대시보드 상단의 테마 버튼에서 시스템, 다크, 라이트 테마를 순서대로 선택할 수 있습니다. 시스템 테마는 운영체제의 다크·라이트 설정 변경을 자동으로 반영하며, 직접 선택한 테마는 같은 브라우저에 저장됩니다.

최초 접속에서는 이름, 이메일, 비밀번호로 관리자 계정을 설정합니다. 최초 관리자 생성 이후에는 추가 가입이 차단되며, 비밀번호는 bcrypt 해시로만 저장됩니다.

## 로컬 실행

Go와 C 컴파일러가 필요합니다.

```bash
go mod download
DB_PATH=./data/submanager.db PORT=8080 TZ=Asia/Seoul go run .
```

테스트는 다음 명령으로 실행합니다.

```bash
go test ./...
```

## 환경변수

| 이름 | 기본값 | 설명 |
|---|---|---|
| `PORT` | `8080` | HTTP 서버 포트 |
| `DB_PATH` | `./data/submanager.db` | SQLite 파일 경로 |
| `TZ` | `Asia/Seoul` | 날짜 계산 timezone |

최초 실행 시 국내에서 자주 쓰이는 기본 서비스 템플릿 18개와 수정할 수 없는 기본 결제수단 5개가 자동으로 생성됩니다. 시드는 중복 생성되지 않습니다.

서로 다른 통화는 환산하거나 합산하지 않고 통화별 합계와 그래프로 표시합니다. 원·엔처럼 소수 단위가 없는 통화는 정수로, 리라·달러 등은 해당 통화의 소수 단위까지 입력할 수 있으며 금액은 오차가 없도록 최소 단위 정수로 저장합니다. 설정의 데이터 관리에서 계정 비밀번호와 로그인 세션을 제외한 앱 데이터를 JSON으로 내보내거나 가져올 수 있습니다.

기본 통화로 KRW, USD, JPY, EUR, TRY, ARS를 제공하며 설정의 통화 관리에서 영문 3자리 코드를 직접 추가할 수 있습니다.
