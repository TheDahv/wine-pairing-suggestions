# AI Agent Instructions

## Project Layout

```
wine-pairing-suggestions/
├── cmd/
│   ├── lambda/        # Lambda entry point (production)
│   └── webapp/        # HTTP server entry point (local dev)
├── webapp/            # Core HTTP handlers and business logic
├── data/              # DynamoDB operations (primary data store)
├── cache/             # Redis/Valkey operations (optional performance layer)
├── models/            # LLM integration (Anthropic Claude)
├── mcp/               # Model Context Protocol tools for recipe fetching
├── helpers/           # Utility functions
├── lambdahelpers/     # AWS Lambda-specific utilities
├── specs/             # Architecture docs and migration plans
└── webapp/templates/  # HTML templates for UI

**Key files:**
- `template.yaml` - AWS SAM CloudFormation template
- `Makefile` - Build, test, and deployment commands
- `docker-compose.yml` - Local development environment
```

## Architecture

**Deployment:** AWS Lambda (Go 1.22+) behind API Gateway with custom domain
**Data:** DynamoDB (primary) + Valkey/Redis cache (optional, feature-flagged)
**LLM:** Anthropic Claude 3.5 Haiku via Bedrock or direct API
**Auth:** Google OAuth with JWT session tokens
**Networking:** Lambda in VPC for private Valkey/DynamoDB access

**Data Strategy:**
- DynamoDB is always checked first (source of truth)
- Cache is gated by `ENABLE_CACHE` environment variable (default: disabled)
- All operations work without cache enabled

**Key Services:**
- **DynamoDB Tables:** `Accounts`, `RecipePairings` (with Type-DateCreated-index GSI)
  - Tables created automatically on first Lambda invocation (not by CloudFormation)
- **Valkey/Redis Cache:** Optional performance layer for frequently accessed data
- **API Gateway HTTP API:** Routes all requests to single Lambda function
- **Custom Domain:** wine-suggestions.thedahv.com with ACM certificate

## AWS SAM Essentials

### Build Process
```bash
make build              # Build Lambda binary (linux/amd64)
make sam-build          # SAM build process
make package            # Package to S3 for deployment
```

**Build artifact:** `bootstrap` (Go binary for Lambda custom runtime `provided.al2023`)

### Deployment
```bash
make deploy             # Full build → package → deploy pipeline
make deploy-guided      # Interactive deployment with parameter prompts
```

**Required parameters** (set in `.env` or retrieved from existing stack):
- `GOOGLE_CLIENT_ID` - OAuth client ID
- `HOSTNAME` - Custom domain (e.g., wine-suggestions.thedahv.com)
- `VALKEY_ENDPOINT` - Redis endpoint (host:port)
- `VPC_ID` - VPC for Lambda deployment
- `SECURITY_GROUP_ID` - Security group for Lambda VPC access
- `SUBNET_IDS` - Comma-separated subnet IDs

**Auto-retrieved secrets:**
- `ANTHROPIC_API_KEY` - Retrieved from AWS Secrets Manager at deployment time

**Stack name:** `wine-pairing-suggestions-lambda`
**S3 bucket:** `wine-pairing-suggestions-sam-deployments` (auto-created if missing)
**Region:** `us-west-2`

### Local Development

**Quick start (recommended):**
```bash
make setup-local        # Clean start: stops containers, starts fresh, creates tables
# OR
docker-compose up -d    # Tables created automatically via init container
```

**Native Go server:**
```bash
# Start dependencies first
docker-compose up -d dynamodb-local cache
sleep 5
make setup-local-db     # Create tables in local DynamoDB

# Run webapp
make build-local && DYNAMODB_ENDPOINT=http://localhost:8000 ./webapp-bin
```

**Manual table setup:**
```bash
make setup-local-db     # Create tables in running DynamoDB Local
```

**Clean database:**
```bash
make clean-local-db     # Stops containers and removes volumes (full reset)
```

**View local services:**
- **DynamoDB Admin:** http://localhost:8001
- **DynamoDB API:** http://localhost:8000
- **Redis:** localhost:6379
- **Health check:** http://localhost:8080/healthz  # Note: /healthz not /health

**Table consistency:** Local tables match production CloudFormation definitions via Makefile

### Template Structure

- **Runtime:** `provided.al2023` (custom Go runtime)
- **Handler:** `bootstrap` (required name for custom runtime)
- **Memory:** 2048 MB
- **Timeout:** 60 seconds
- **Events:** HTTP API with catch-all routes (`/` and `/{proxy+}`)
- **VPC:** Lambda deployed in private subnets with security group

**Key CloudFormation resources:**
- `WinePairingFunction` - Lambda function
- `ServerlessHttpApi` - HTTP API Gateway (auto-created by SAM)
- `CustomDomain` - API Gateway custom domain with ACM cert
- `PathMapping` - Maps custom domain to API

### Logs
```bash
# Get actual Lambda function name (SAM adds random suffix like -wXaZnWHxvkUq)
FUNCTION_NAME=$(aws cloudformation describe-stack-resource \
  --stack-name wine-pairing-suggestions-lambda \
  --logical-resource-id WinePairingFunction \
  --region us-west-2 \
  --query 'StackResourceDetail.PhysicalResourceId' \
  --output text)

# View recent logs
aws logs tail /aws/lambda/$FUNCTION_NAME --follow --region us-west-2
```

### Inspecting Deployed Stack

**Check stack status:**
```bash
aws cloudformation describe-stacks \
  --stack-name wine-pairing-suggestions-lambda \
  --region us-west-2 \
  --query 'Stacks[0].[StackStatus,LastUpdatedTime]' \
  --output table
```

**List deployed resources:**
```bash
aws cloudformation describe-stack-resources \
  --stack-name wine-pairing-suggestions-lambda \
  --region us-west-2 \
  --query 'StackResources[*].[LogicalResourceId,ResourceType,ResourceStatus]' \
  --output table
```

**View stack outputs (API URLs, endpoints):**
```bash
aws cloudformation describe-stacks \
  --stack-name wine-pairing-suggestions-lambda \
  --region us-west-2 \
  --query 'Stacks[0].Outputs' \
  --output table
```

**SAM CLI introspection:**
```bash
sam list resources --stack-name wine-pairing-suggestions-lambda --region us-west-2
sam list endpoints --stack-name wine-pairing-suggestions-lambda --region us-west-2
sam logs --stack-name wine-pairing-suggestions-lambda --tail --region us-west-2
```

## Common Commands

```bash
# Deployment
make build              # Build Lambda binary
make deploy             # Deploy to AWS (uses .env + existing params)

# Local development
make setup-local        # Fresh local environment with tables
make setup-local-db     # Create/verify tables in local DynamoDB
make clean-local-db     # Remove local DynamoDB data (full reset)
make build-local        # Build native binary for development
make run-local          # Run webapp locally (port 8080)
make run-docker-bg      # Start Docker dev environment

# Maintenance
make clean-all          # Clean builds + Docker volumes
make test               # Run Go tests
```

## Environment Variables

**Deployment-critical (AWS Lambda):**
- `GOOGLE_CLIENT_ID` - Google OAuth configuration
- `HOSTNAME` - Custom domain for OAuth redirects
- `VALKEY_ENDPOINT` - Cache endpoint (host:port format)
- `ANTHROPIC_API_KEY` - LLM API key (auto-retrieved from Secrets Manager in prod)

**Feature flags:**
- `ENABLE_CACHE` - Set to "true" to enable cache layer (default: disabled)

**Local development:**
- `DYNAMODB_ENDPOINT=http://localhost:8000` - Use local DynamoDB
- `VALKEY_ENDPOINT=localhost:6379` - Use local Redis

See `specs/codebase-guide.md` Quick Reference section for complete environment variable list.

## Troubleshooting

**Test production endpoints:**
```bash
# Custom domain health check
curl https://wine-suggestions.thedahv.com/healthz

# Get direct API Gateway URL from stack outputs
aws cloudformation describe-stacks \
  --stack-name wine-pairing-suggestions-lambda \
  --region us-west-2 \
  --query 'Stacks[0].Outputs[?OutputKey==`WinePairingAPI`].OutputValue' \
  --output text
```

**SAM resource naming:** SAM generates function names as `{StackName}-{LogicalId}-{RandomHash}`
- Example: `wine-pairing-suggestions-lambd-WinePairingFunction-wXaZnWHxvkUq`
- Use `aws cloudformation describe-stack-resource` to get actual physical resource IDs

**DynamoDB tables not found:** Tables are created by CloudFormation (prod) or Makefile/docker-compose (local) before the application starts. The application validates tables exist via `ValidateTables()` on startup.

## Additional Context

**For quick tasks:** This file has everything you need for deployment and operations.

**For complex changes**, see `specs/codebase-guide.md`:
- **Code patterns and examples** (Section 6: Common Patterns)
- **Pitfalls to avoid** (Section 8: wrong vs. correct examples)
- **Migration history** (Section 5: DynamoDB migration status)
- **Performance analysis** (Section 7: costs, bottlenecks)
- **Complete API reference** (Section 11: all endpoints)

**For migration plans**, see `specs/migrate-data-layer.md`
