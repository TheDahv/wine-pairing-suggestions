# Project Analysis and DynamoDB Table Generation Prompt

You are tasked with analyzing a local AWS SAM project and automatically
generating the necessary DynamoDB table configurations.  Here are the critical
details about the existing infrastructure:

## AWS Account & Stack Information
- AWS Account ID: 520805538041
- Region: us-west-2
- Stack Name: wine-pairing-suggestions-lambda
- Stack ARN: arn:aws:cloudformation:us-west-2:520805538041:stack/wine-pairing-suggestions-lambda/71f5adb0-70f5-11f0-a3d1-0213cb6e89f7

## Existing Resources
- Lambda Function ARN: arn:aws:lambda:us-west-2:520805538041:function:wine-pairing-suggestions-lambd-WinePairingFunction-wXaZnWHxvkUq
- IAM Role: wine-pairing-suggestions-la-WinePairingFunctionRole-v8LuInqiKCCo
- API Gateway: https://t4qxd9jkz4.execute-api.us-west-2.amazonaws.com 
- Custom Domain: wine-suggestions.thedahv.com
- VPC Subnets: subnet-0d7bec672ecc67b69, subnet-0fc5205a59aa350ab
- Security Group: sg-70f2021f

# Task Instructions

1. Scan the local project directory for:
  - Code files that indicate data models or database operations
  - Existing SAM template.yaml or template.yml files
  - Configuration files that might reference data storage needs
  - API endpoints that suggest CRUD operations
2. Analyze data patterns to determine:
  - Primary key structures needed
  - Secondary indexes required
  - Read/write capacity requirements
  - Table relationships and access patterns
3. Generate DynamoDB table definitions using:
  - AWS::Serverless::SimpleTable for simple single-key tables
  - AWS::DynamoDB::Table for complex tables with GSI/LSI requirements
  - Appropriate naming conventions following the existing stack pattern
4. Add IAM permissions by:
  - Extending the existing IAM role with DynamoDB permissions
  - Using SAM Connectors to link Lambda functions to tables
  - Following least-privilege access principles
5. Update the SAM template with:
  - New table resources in the Resources section
  - Environment variables for table names in Lambda functions
  - Proper cross-references using !Ref and !GetAtt

# Required Documentation References
Study these AWS documentation links before proceeding:
- SAM DynamoDB Integration:
  - https://docs.aws.amazon.com/serverless-application-model/latest/developerguide/sam-resource-simpletable.html
  - https://docs.aws.amazon.com/serverless-application-model/latest/developerguide/sam-resource-connector.html
- DynamoDB Best Practices:
  - https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-resource-dynamodb-table.html
  - https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/best-practices.html
- SAM Template Structure:
  - https://docs.aws.amazon.com/serverless-application-model/latest/developerguide/sam-specification-template-anatomy.html
  - https://docs.aws.amazon.com/serverless-application-model/latest/developerguide/authoring-define-resources.html

Output Requirements
Generate a complete SAM template snippet that includes:
1. DynamoDB table definitions with appropriate properties
1. IAM policy updates or Connector configurations
1. Environment variables for the Lambda function
1. Comments explaining the design decisions
1. Deployment instructions for updating the existing stack

Safety Considerations
- Use DeletionPolicy: Retain for production tables
- Include Point-in-Time Recovery configuration
- Set appropriate billing modes (`PAY_PER_REQUEST` recommended for variable workloads)
- Ensure table names are unique and follow naming conventions

Analyze the project thoroughly and provide a production-ready configuration that
integrates seamlessly with the existing wine-pairing-suggestions-lambda stack.
