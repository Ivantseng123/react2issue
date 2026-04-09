# K8s Deployment & CI/CD Design

## Overview

Add Kubernetes deployment manifests (Kustomize) and Jenkins CI/CD pipelines for the react2issue bot. The bot runs as a single pod with Socket Mode (no ingress needed), spawns `claude` CLI as a subprocess for codebase analysis.

## Dockerfile

Multi-stage build: Go binary + Node.js runtime (for claude CLI) + gh CLI.

```dockerfile
# ---- Build stage ----
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache git
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o bot ./cmd/bot

# ---- Runtime stage ----
FROM node:22-alpine

RUN apk add --no-cache git ca-certificates

# Install claude CLI globally
RUN npm install -g @anthropic-ai/claude-code

# Install gh CLI (for agent to create issues)
RUN apk add --no-cache github-cli

# Go binary
COPY --from=builder /app/bot /bot

# Agent skills
COPY agents/skills/ /opt/agents/skills/
RUN mkdir -p /home/node/.claude/skills && \
    for d in /opt/agents/skills/*/; do \
      ln -s "$d" /home/node/.claude/skills/$(basename "$d"); \
    done

# Config (default, can be overridden via ConfigMap volume mount)
COPY config.example.yaml /config.yaml

USER node
ENTRYPOINT ["/bot"]
CMD ["-config", "/config.yaml"]
```

Key decisions:
- Base image: `node:22-alpine` (claude CLI requires Node.js)
- `gh` CLI via apk (agent creates issues with `gh issue create`)
- Skills symlinked at build time (same pattern as local dev)
- Runs as `node` user (non-root, UID matches node image default)
- Auth tokens injected via env vars at runtime (not baked in)

## Kustomize Structure

```
deploy/
  base/                              # In repo, no sensitive info
    kustomization.yaml
    deployment.yaml
    service.yaml
  overlays/
    example/                         # In repo, placeholder values
      kustomization.yaml.example
      configmap.yaml.example
      secret.yaml.example
    <env>/                           # Gitignored, actual deploy config
      kustomization.yaml
      configmap.yaml
      secret.yaml
```

### Base Layer

**kustomization.yaml:**
```yaml
labels:
  - includeSelectors: true
    pairs:
      app: APP_NAME
resources:
  - deployment.yaml
  - service.yaml
```

**deployment.yaml:**
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: APP_NAME
spec:
  replicas: 1
  strategy:
    type: RollingUpdate
  template:
    spec:
      imagePullSecrets:
        - name: regcred
      containers:
        - name: APP_NAME
          image: IMAGE_PLACEHOLDER
          imagePullPolicy: Always
          ports:
            - containerPort: 8180
              name: http
              protocol: TCP
          livenessProbe:
            httpGet:
              path: /healthz
              port: http
            timeoutSeconds: 5
            failureThreshold: 5
          readinessProbe:
            httpGet:
              path: /healthz
              port: http
            initialDelaySeconds: 3
            timeoutSeconds: 5
          resources:
            limits:
              cpu: "1"
              memory: 1Gi
            requests:
              cpu: 100m
              memory: 256Mi
          envFrom:
            - configMapRef:
                name: APP_NAME-config
            - secretRef:
                name: APP_NAME-secret
  revisionHistoryLimit: 0
```

**service.yaml:**
```yaml
apiVersion: v1
kind: Service
metadata:
  name: APP_NAME
spec:
  type: ClusterIP
  ports:
    - name: http
      port: 8180
      protocol: TCP
```

### Overlay Example

**kustomization.yaml.example:**
```yaml
labels:
  - includeSelectors: true
    pairs:
      app: react2issue
namespace: your-namespace
resources:
  - ../../base
  - configmap.yaml
  - secret.yaml
images:
  - name: IMAGE_PLACEHOLDER
    newName: your-registry.example.com/your-org/react2issue
    newTag: "0.1.0"
```

**configmap.yaml.example:**
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: APP_NAME-config
data:
  GH_TOKEN: "ghp_your-token"
  SLACK_BOT_TOKEN: "xoxb-your-token"
  SLACK_APP_TOKEN: "xapp-your-token"
```

**secret.yaml.example:**
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: APP_NAME-secret
type: Opaque
stringData:
  CLAUDE_AUTH_TOKEN: "your-claude-setup-token"
```

### Gitignore

```
deploy/overlays/*/
!deploy/overlays/example/
```

### Key Differences from Reference Project (jasmine-policy)

- No ingress (Socket Mode, no public URL needed)
- No startupProbe / liveness-patch (Go binary starts instantly)
- Secret added for claude auth token
- Smaller resources (Go binary vs Spring Boot JVM)
- Port 8180 (matches bot's health check config)

## Jenkins CI/CD

### Jenkinsfile-bump-version

Triggered manually with parameters: `version` (semver), `ref` (target branch).

Flow:
1. Setup: git auth + gh auth
2. Check Tag: validate semver, update kustomization.yaml.example newTag
3. Unit Testing: `go test ./...`
4. Bump New Version: create PR, auto-merge

Agent pod: `golang:1.25-alpine` + `git` containers. No Maven, no PVC.

### Jenkinsfile-release

Triggered manually with parameters: `tag` (semver), `ref` (target branch).

Flow:
1. Setup: git auth + gh auth
2. Preflight Checks: validate semver, check tag doesn't exist
3. Build & Push Image: `docker build` + `docker push` to registry
4. Publish GitHub Release: `gh release create` with auto-generated notes

Agent pod: `golang:1.25-alpine` + `git` + `docker:24` containers. Docker socket mounted for image build.

### Key Differences from Reference Project

- `golang` container replaces `maven` (no m2 PVC needed)
- `docker` container added for image build/push
- No redoc-cli, curl-jq (no OpenAPI)
- No artifact repo deploy (Go doesn't have Maven Central equivalent)
- Registry and image name via Jenkins env vars (not hardcoded)
- Credential ID uses placeholder

## Config Override Strategy

The bot reads `config.yaml` at startup. In k8s:

1. `config.example.yaml` is baked into the image as `/config.yaml`
2. Overlay can mount a ConfigMap as a volume to replace `/config.yaml`
3. Env vars (`SLACK_BOT_TOKEN`, `GH_TOKEN`, `CLAUDE_AUTH_TOKEN`) are injected separately

The bot's `config.Load()` already supports reading from a file path. Sensitive tokens in the config file (Slack, GitHub) can be overridden by env vars in the overlay's ConfigMap/Secret.

## Claude CLI Authentication in K8s

1. Locally: `claude setup-token` to generate a long-lived token
2. Store token as k8s Secret (`CLAUDE_AUTH_TOKEN`)
3. Pod env var injects the token → claude CLI authenticates with Max plan
4. No interactive login needed in the pod
