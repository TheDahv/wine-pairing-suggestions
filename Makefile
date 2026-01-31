.PHONY: build clean deploy package test load-env check-bucket deploy-info redis-up redis-down test-local-full setup-local-db clean-local-db setup-local

# Configuration
STACK_NAME := wine-pairing-suggestions-lambda
DEFAULT_S3_BUCKET := wine-pairing-suggestions-sam-deployments
AWS_REGION := us-west-2
LAMBDA_BIN := bootstrap
WEBAPP_BIN := webapp-bin

# Load environment variables from .env file if it exists
load-env:
	@if [ -f .env ]; then \
		echo "Loading environment variables from .env file..."; \
		export $$(grep -v '^#' .env | grep -v '^$$' | xargs); \
	fi

# Build the Lambda function
build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o $(LAMBDA_BIN) ./cmd/lambda
	
build-WinePairingFunction:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o $(LAMBDA_BIN) ./cmd/lambda
	
# Build for local testing
build-local:
	go build -o $(WEBAPP_BIN) ./cmd/webapp

# Clean build artifacts
clean:
	rm -f $(LAMBDA_BIN) $(WEBAPP_BIN) packaged-template.yaml

# Clean everything including Docker
clean-all: clean
	@echo "🧹 Cleaning Docker containers and volumes..."
	docker-compose down -v
	@echo "✅ All cleaned up"

# Check if S3 bucket exists, create if it doesn't
check-bucket:
	@if [ -f .env ]; then export $$(grep -v '^#' .env | grep -v '^$$' | xargs); fi; \
	BUCKET=$${S3_BUCKET:-$(DEFAULT_S3_BUCKET)}; \
	echo "📦 Checking deployment bucket: $$BUCKET"; \

	@if aws s3 ls "s3://$$BUCKET" >/dev/null 2>&1; then \
		echo "✅ S3 bucket $$BUCKET exists."; \
	else \
	 	echo "📦 Creating S3 bucket: $$BUCKET"; \
	 	aws s3 mb "s3://$$BUCKET" --region "$(AWS_REGION)"; \
	fi

sam-build:
	echo "📦 Building application..."; \
	sam build

# Package for SAM deployment
package: check-bucket sam-build 
	@if [ -f .env ]; then export $$(grep -v '^#' .env | grep -v '^$$' | xargs); fi; \
	BUCKET=$${S3_BUCKET:-$(DEFAULT_S3_BUCKET)}; \
	echo "📦 Packaging application..."; \
	sam package --template-file template.yaml --s3-bucket "$$BUCKET" --output-template-file packaged-template.yaml --region "$(AWS_REGION)"

# Deploy using SAM with parameter retrieval
deploy: package
	@if [ -f .env ]; then export $$(grep -v '^#' .env | grep -v '^$$' | xargs); fi; \
	\
	echo "🔍 Checking if stack $(STACK_NAME) exists and retrieving parameters..."; \
	if aws cloudformation describe-stacks --stack-name "$(STACK_NAME)" --region "$(AWS_REGION)" >/dev/null 2>&1; then \
		echo "Stack exists, retrieving current parameters..."; \
		EXISTING_GOOGLE_CLIENT_ID=$$(aws cloudformation describe-stacks --stack-name "$(STACK_NAME)" --region "$(AWS_REGION)" --query 'Stacks[0].Parameters[?ParameterKey==`GoogleClientID`].ParameterValue' --output text 2>/dev/null || echo ""); \
		EXISTING_HOSTNAME=$$(aws cloudformation describe-stacks --stack-name "$(STACK_NAME)" --region "$(AWS_REGION)" --query 'Stacks[0].Parameters[?ParameterKey==`Hostname`].ParameterValue' --output text 2>/dev/null || echo ""); \
		EXISTING_VALKEY_ENDPOINT=$$(aws cloudformation describe-stacks --stack-name "$(STACK_NAME)" --region "$(AWS_REGION)" --query 'Stacks[0].Parameters[?ParameterKey==`ValkeyEndpoint`].ParameterValue' --output text 2>/dev/null || echo ""); \
		EXISTING_VPC_ID=$$(aws cloudformation describe-stacks --stack-name "$(STACK_NAME)" --region "$(AWS_REGION)" --query 'Stacks[0].Parameters[?ParameterKey==`VpcId`].ParameterValue' --output text 2>/dev/null || echo ""); \
		EXISTING_SECURITY_GROUP_ID=$$(aws cloudformation describe-stacks --stack-name "$(STACK_NAME)" --region "$(AWS_REGION)" --query 'Stacks[0].Parameters[?ParameterKey==`SecurityGroupId`].ParameterValue' --output text 2>/dev/null || echo ""); \
		EXISTING_SUBNET_IDS=$$(aws cloudformation describe-stacks --stack-name "$(STACK_NAME)" --region "$(AWS_REGION)" --query 'Stacks[0].Parameters[?ParameterKey==`SubnetIds`].ParameterValue' --output text 2>/dev/null || echo ""); \
	fi; \
	\
	FINAL_GOOGLE_CLIENT_ID=$${GOOGLE_CLIENT_ID_OVERRIDE:-$${GOOGLE_CLIENT_ID:-$$EXISTING_GOOGLE_CLIENT_ID}}; \
	FINAL_HOSTNAME=$${HOSTNAME_OVERRIDE:-$${HOSTNAME:-$$EXISTING_HOSTNAME}}; \
	FINAL_VALKEY_ENDPOINT=$${VALKEY_ENDPOINT_OVERRIDE:-$${VALKEY_ENDPOINT:-$$EXISTING_VALKEY_ENDPOINT}}; \
	FINAL_VPC_ID=$${VPC_ID_OVERRIDE:-$${VPC_ID:-$$EXISTING_VPC_ID}}; \
	FINAL_SECURITY_GROUP_ID=$${SECURITY_GROUP_ID_OVERRIDE:-$${SECURITY_GROUP_ID:-$$EXISTING_SECURITY_GROUP_ID}}; \
	FINAL_SUBNET_IDS=$${SUBNET_IDS_OVERRIDE:-$${SUBNET_IDS:-$$EXISTING_SUBNET_IDS}}; \
	\
	if [ -z "$$FINAL_GOOGLE_CLIENT_ID" ] || [ -z "$$FINAL_HOSTNAME" ] || [ -z "$$FINAL_VALKEY_ENDPOINT" ] || [ -z "$$FINAL_VPC_ID" ] || [ -z "$$FINAL_SECURITY_GROUP_ID" ] || [ -z "$$FINAL_SUBNET_IDS" ]; then \
		echo "❌ Missing required parameters. Please set in .env file or as environment variables:"; \
		echo "  - GOOGLE_CLIENT_ID"; \
		echo "  - HOSTNAME"; \
		echo "  - VALKEY_ENDPOINT"; \
		echo "  - VPC_ID"; \
		echo "  - SECURITY_GROUP_ID"; \
		echo "  - SUBNET_IDS"; \
		exit 1; \
	fi; \
	\
	ANTHROPIC_API_KEY=$$(aws secretsmanager get-secret-value --secret-id prod/Anthropic/WineSuggestions --region $(AWS_REGION) --query SecretString --output text | jq -r .ANTHROPIC_WINESUGGESTIONS); \
	\
	echo "🚀 Deploying $(STACK_NAME) in bucket $(DEFAULT_S3_BUCKET) to AWS..."; \
	sam deploy \
	 	--config-file samconfig.toml \
		--template-file packaged-template.yaml \
		--no-resolve-s3 \
		--s3-bucket "$(DEFAULT_S3_BUCKET)" \
		--stack-name "$(STACK_NAME)" \
		--capabilities CAPABILITY_IAM \
		--region "$(AWS_REGION)" \
		--parameter-overrides \
			AnthropicApiKey="$$ANTHROPIC_API_KEY" \
			GoogleClientID="$$FINAL_GOOGLE_CLIENT_ID" \
			Hostname="$$FINAL_HOSTNAME" \
			ValkeyEndpoint="$$FINAL_VALKEY_ENDPOINT" \
			VpcId="$$FINAL_VPC_ID" \
			SecurityGroupId="$$FINAL_SECURITY_GROUP_ID" \
			SubnetIds="$$FINAL_SUBNET_IDS"
	\
	$(MAKE) deploy-info

# Get deployment info after successful deployment
deploy-info:
	@if [ -f .env ]; then export $$(grep -v '^#' .env | grep -v '^$$' | xargs); fi; \
	\
	echo ""; \
	echo "✅ Deployment successful!"; \
	echo ""; \
	API_URL=$$(aws cloudformation describe-stacks --stack-name "$(STACK_NAME)" --region "$(AWS_REGION)" --query 'Stacks[0].Outputs[?OutputKey==`WinePairingAPI`].OutputValue' --output text 2>/dev/null || echo "N/A"); \
	CUSTOM_DOMAIN_TARGET=$$(aws cloudformation describe-stacks --stack-name "$(STACK_NAME)" --region "$(AWS_REGION)" --query 'Stacks[0].Outputs[?OutputKey==`CustomDomainEndpoint`].OutputValue' --output text 2>/dev/null || echo "N/A"); \
	LAMBDA_ARN=$$(aws cloudformation describe-stacks --stack-name "$(STACK_NAME)" --region "$(AWS_REGION)" --query 'Stacks[0].Outputs[?OutputKey==`LambdaFunction`].OutputValue' --output text 2>/dev/null || echo "N/A"); \
	\
	echo "📡 API Gateway URL: $$API_URL"; \
	echo "🌐 Custom Domain Target: $$CUSTOM_DOMAIN_TARGET"; \
	echo "⚡ Lambda Function ARN: $$LAMBDA_ARN"; \
	echo ""; \
	echo "💡 Next steps:"; \
	echo "  1. In your DNS provider, create or update a CNAME record for your custom domain to point to the 'Custom Domain Target': $$CUSTOM_DOMAIN_TARGET"; \
	echo "  2. Ensure your OAuth redirect URIs use your custom domain: https://$$(HOSTNAME)"; \
	echo "  3. Test the custom domain: curl https://$$(HOSTNAME)/healthz"; \
	echo "  4. Monitor logs: aws logs tail /aws/lambda/$$(aws cloudformation describe-stack-resource --stack-name $(STACK_NAME) --logical-resource-id WinePairingFunction --query 'StackResourceDetail.PhysicalResourceId' --output text) --follow";

# Deploy using SAM with guided prompts
deploy-guided:
	@if [ -f .env ]; then export $$(grep -v '^#' .env | grep -v '^$$' | xargs); fi; \
	ANTHROPIC_API_KEY=$$(aws secretsmanager get-secret-value --secret-id prod/Anthropic/WineSuggestions --region $(AWS_REGION) --query SecretString --output text | jq -r .ANTHROPIC_WINESUGGESTIONS); \
	sam deploy \
		--stack-name $(STACK_NAME) \
		--s3-bucket $(DEFAULT_S3_BUCKET) \
		--no-resolve-s3 \
		--guided

# Start local Redis via docker-compose
redis-up:
	@echo "🚀 Starting local Redis container..."
	docker-compose up -d cache
	@echo "✅ Redis is running at localhost:6379"

# Stop local Redis
redis-down:
	@echo "🛑 Stopping local Redis container..."
	docker-compose down cache
	@echo "✅ Redis stopped"

# Test locally with SAM (using in-memory cache)
test-local: build
	@if [ -f .env ]; then export $$(grep -v '^#' .env | grep -v '^$$' | xargs); fi; \
	echo "🚀 Starting SAM local API (connects to localhost:6379)..."; \
	VALKEY_ENDPOINT=localhost:6379 sam local start-api

# Test locally with SAM connected to Docker Redis network
test-local-docker: build sam-build
	@if [ -f .env ]; then export $$(grep -v '^#' .env | grep -v '^$$' | xargs); fi; \
	echo "🚀 Starting SAM local API connected to Docker network..."; \
	HOSTNAME=http://localhost:3000 \
		VALKEY_ENDPOINT=cache:6379 \
		DYNAMODB_ENDPOINT=http://dynamodb-local:8000 \
		sam local start-api \
			--docker-network wine-pairing-suggestions_wine-net \
			-t template.yaml \
			--host 0.0.0.0

# Full local development setup (Redis + SAM)
test-local-full: redis-up
	@echo "⏳ Waiting for Redis to be ready..."
	@sleep 2
	@$(MAKE) test-local-docker

# Run unit tests
test:
	go test ./...

# Run linting
lint:
	golangci-lint run

# Build and run the traditional web server locally (with host Redis)
run-local: build-local
	@if [ -f .env ]; then export $$(grep -v '^#' .env | grep -v '^$$' | xargs); fi; \
	VALKEY_ENDPOINT=localhost:6379 ./webapp

# Run the full docker-compose stack
run-docker:
	@echo "🚀 Starting full Docker Compose stack..."
	docker-compose up

# Run the full docker-compose stack in background
run-docker-bg:
	@echo "🚀 Starting full Docker Compose stack in background..."
	docker-compose up -d

# Setup local DynamoDB tables (matches CloudFormation definitions in template.yaml)
setup-local-db:
	@echo "🗄️  Setting up local DynamoDB tables..."
	@ENDPOINT="http://localhost:8000"; \
	REGION="us-west-2"; \
	\
	echo "⏳ Waiting for DynamoDB Local..."; \
	until aws dynamodb list-tables --endpoint-url $$ENDPOINT --region $$REGION >/dev/null 2>&1; do \
		echo "   DynamoDB Local not ready, retrying in 2s..."; \
		sleep 2; \
	done; \
	echo "✅ DynamoDB Local is ready"; \
	\
	echo "   Creating Accounts table..."; \
	aws dynamodb describe-table --table-name Accounts --endpoint-url $$ENDPOINT --region $$REGION >/dev/null 2>&1 || \
	aws dynamodb create-table \
		--table-name Accounts \
		--billing-mode PAY_PER_REQUEST \
		--attribute-definitions AttributeName=ID,AttributeType=S \
		--key-schema AttributeName=ID,KeyType=HASH \
		--endpoint-url $$ENDPOINT --region $$REGION >/dev/null; \
	echo "   ✅ Accounts table ready"; \
	\
	echo "   Creating RecipePairings table..."; \
	aws dynamodb describe-table --table-name RecipePairings --endpoint-url $$ENDPOINT --region $$REGION >/dev/null 2>&1 || \
	aws dynamodb create-table \
		--table-name RecipePairings \
		--billing-mode PAY_PER_REQUEST \
		--attribute-definitions \
			AttributeName=ID,AttributeType=S \
			AttributeName=Type,AttributeType=S \
			AttributeName=DateCreated,AttributeType=S \
		--key-schema AttributeName=ID,KeyType=HASH \
		--global-secondary-indexes \
			'IndexName=Type-DateCreated-index,KeySchema=[{AttributeName=Type,KeyType=HASH},{AttributeName=DateCreated,KeyType=RANGE}],Projection={ProjectionType=KEYS_ONLY}' \
		--endpoint-url $$ENDPOINT --region $$REGION >/dev/null; \
	echo "   ✅ RecipePairings table ready"; \
	\
	echo "🎉 Local DynamoDB setup complete!"; \
	aws dynamodb list-tables --endpoint-url $$ENDPOINT --region $$REGION --output table

# Clean local DynamoDB data
clean-local-db:
	@echo "🧹 Cleaning local DynamoDB data..."
	docker-compose down -v
	@echo "✅ Local DynamoDB data cleared"

# Full local setup with fresh database
setup-local: clean-local-db
	@echo "🚀 Starting fresh local environment..."
	docker-compose up -d
	@echo "⏳ Waiting for services to be ready..."
	sleep 5
	$(MAKE) setup-local-db
	@echo "✅ Local environment ready!"
	@echo "   DynamoDB Local: http://localhost:8000"
	@echo "   DynamoDB Admin: http://localhost:8001"
	@echo "   Redis: localhost:6379"

# Environment variable examples
example-env:
	@echo "Example environment variables for deployment (.env file or environment):"
	@echo "S3_BUCKET=your-sam-deployment-bucket"
	@echo "GOOGLE_CLIENT_ID=your-google-client-id"
	@echo "HOSTNAME=your-custom-domain.com"
	@echo "VALKEY_ENDPOINT=your-valkey-endpoint:6379"
	@echo "VPC_ID=vpc-xxxxxxxx"
	@echo "SECURITY_GROUP_ID=sg-xxxxxxxx"
	@echo "SUBNET_IDS=subnet-xxx,subnet-yyy,subnet-zzz"
