package data

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type DataLayer struct {
	client *dynamodb.Client
}

var ErrNotFound = errors.New("item not found")

type Account struct {
	ID    string `dynamodbav:"ID"`
	Email string `dynamodbav:"Email"`
	Quota int    `dynamodbav:"Quota"`
}

type PairingType string

const (
	PairingTypeURL         PairingType = "URL"
	PairingTypeContentHash PairingType = "ContentHash"
)

type RecipePairing struct {
	ID          string       `dynamodbav:"ID"`
	Type        PairingType  `dynamodbav:"Type"` // "URL" or "ContentHash"
	DateCreated time.Time    `dynamodbav:"DateCreated"`
	Summary     string       `dynamodbav:"Summary"`
	Suggestions []Suggestion `dynamodbav:"Suggestions"`
}

type Suggestion struct {
	Style       string `dynamodbav:"Style"`
	Region      string `dynamodbav:"Region"`
	Description string `dynamodbav:"Description"`
	PairingNote string `dynamodbav:"PairingNote"`
}

func Create(ctx context.Context) (*DataLayer, error) {
	l := log.New(log.Default().Writer(), "[DataLayer.Create]", log.Default().Flags())
	dl := &DataLayer{}

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return dl, fmt.Errorf("unable to create database config: %v", err)
	}
	dl.client = dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		l.Println("Checking DB env:", os.Getenv("DYNAMODB_ENDPOINT"))
		if endpoint := os.Getenv("DYNAMODB_ENDPOINT"); endpoint != "" {
			l.Println("Connecting to local DynamoDB at:", endpoint)
			o.BaseEndpoint = aws.String(endpoint)
		}
	})
	return dl, nil
}

func (dl *DataLayer) ValidateTables(ctx context.Context) error {
	l := log.New(log.Default().Writer(), "[DataLayer.ValidateTables]", log.Default().Flags())

	l.Println("Verifying required tables exist...")
	l.Println("Note: Tables should be created by CloudFormation (prod) or Makefile/docker-compose (local)")

	requiredTables := []string{"Accounts", "RecipePairings"}

	for _, tableName := range requiredTables {
		result, err := dl.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{
			TableName: aws.String(tableName),
		})
		if err != nil {
			return fmt.Errorf("table %s not found - ensure tables are created before starting application: %w", tableName, err)
		}
		l.Printf("✓ Table %s exists (status: %s)\n", tableName, result.Table.TableStatus)
	}

	l.Println("All required tables verified")
	return nil
}

const defaultQuota = 10

// In data/data.go

// --- Account Functions ---

// GetAccountByID retrieves an account by its unique ID (the Google 'sub' claim).
// It should return ErrNotFound if the account does not exist.
func (dl *DataLayer) GetAccountByID(ctx context.Context, id string) (Account, error) {
	var account Account

	// Use Query instead of GetItem. This allows us to find the account using only the
	// Partition Key (ID), even if the table has a Sort Key (e.g., Email) configured
	// that we don't know at this point.
	input := &dynamodb.QueryInput{
		TableName:              aws.String("Accounts"),
		KeyConditionExpression: aws.String("ID = :id"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":id": &types.AttributeValueMemberS{Value: id},
		},
		Limit: aws.Int32(1),
	}

	result, err := dl.client.Query(ctx, input)
	if err != nil {
		return account, fmt.Errorf("failed to query account: %w", err)
	}

	if len(result.Items) == 0 {
		return account, ErrNotFound
	}

	if err = attributevalue.UnmarshalMap(result.Items[0], &account); err != nil {
		return account, fmt.Errorf("failed to unmarshal account item: %w", err)
	}

	return account, nil
}

// CreateAccount creates a new user account with a default quota if it doesn't already exist.
// If the account exists, it should simply return the existing account details.
func (dl *DataLayer) CreateAccount(ctx context.Context, id string, email string) (Account, error) {
	fmt.Printf("[CreateAccount] Creating for email=%s, id=%s\n", email, id)
	existingAccount, err := dl.GetAccountByID(ctx, id)
	if err == nil {
		fmt.Println("[CreateAccount] Found existing")
		return existingAccount, nil
	}
	if !errors.Is(err, ErrNotFound) {
		fmt.Println("[CreateAccount] Found error that wasn't NotFound error")
		return Account{}, fmt.Errorf("error checking for existing account: %w", err)
	}

	newAccount := Account{
		ID:    id,
		Email: email,
		Quota: defaultQuota,
	}
	fmt.Println("[CreateAccount] Set up new account to save")
	fmt.Println(newAccount)

	item, err := attributevalue.MarshalMap(newAccount)
	if err != nil {
		return Account{}, fmt.Errorf("failed to marshal new account: %w", err)
	}

	_, err = dl.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String("Accounts"),
		Item:      item,
	})

	if err != nil {
		return Account{}, fmt.Errorf("failed to create account in dynamodb: %w", err)
	}

	return newAccount, nil
}

// DecrementAccountQuota reduces the suggestion quota for a given account ID by one.
func (dl *DataLayer) DecrementAccountQuota(ctx context.Context, id string) error {
	key, err := attributevalue.MarshalMap(map[string]string{"ID": id})
	if err != nil {
		return fmt.Errorf("failed to marshal key for DecrementAccountQuota: %w", err)
	}

	update := types.Update{
		TableName:        aws.String("Accounts"),
		Key:              key,
		UpdateExpression: aws.String("SET Quota = Quota - :val"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":val": &types.AttributeValueMemberN{Value: "1"},
			":min": &types.AttributeValueMemberN{Value: "0"},
		},
		ConditionExpression: aws.String("Quota > :min"),
	}

	_, err = dl.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 update.TableName,
		Key:                       update.Key,
		UpdateExpression:          update.UpdateExpression,
		ExpressionAttributeValues: update.ExpressionAttributeValues,
		ConditionExpression:       update.ConditionExpression,
	})

	if err != nil {
		var ccf *types.ConditionalCheckFailedException
		if errors.As(err, &ccf) {
			return fmt.Errorf("cannot decrement quota, it is already at or below zero")
		}
		return fmt.Errorf("failed to decrement account quota: %w", err)
	}

	return nil
}

// ResetAllAccountQuotas scans all accounts and resets their quota to the default value.
// This is useful for periodic quota refreshes (e.g., weekly).
func (dl *DataLayer) ResetAllAccountQuotas(ctx context.Context) error {
	l := log.New(log.Default().Writer(), "[DataLayer.ResetAllAccountQuotas]", log.Default().Flags())
	paginator := dynamodb.NewScanPaginator(dl.client, &dynamodb.ScanInput{
		TableName: aws.String("Accounts"),
	})

	accountsProcessed := 0

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("failed to get page of accounts: %w", err)
		}

		for _, item := range page.Items {
			var acc Account
			if err := attributevalue.UnmarshalMap(item, &acc); err != nil {
				l.Printf("failed to unmarshal account, skipping: %v", err)
				continue
			}

			// Use an atomic UpdateItem call to prevent race conditions.
			// This is safer than the read-modify-write pattern with PutItem.
			key, err := attributevalue.MarshalMap(map[string]string{"ID": acc.ID})
			if err != nil {
				l.Printf("failed to marshal key for account %s, skipping: %v", acc.ID, err)
				continue
			}

			updateExpression := "SET Quota = :q"
			expressionValues, err := attributevalue.MarshalMap(map[string]interface{}{
				":q": defaultQuota,
			})
			if err != nil {
				l.Printf("failed to marshal expression values for account %s, skipping: %v", acc.ID, err)
				continue
			}

			_, err = dl.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
				TableName:                 aws.String("Accounts"),
				Key:                       key,
				UpdateExpression:          aws.String(updateExpression),
				ExpressionAttributeValues: expressionValues,
			})

			if err != nil {
				l.Printf("failed to update quota for account %s, skipping: %v", acc.ID, err)
				continue // Continue to the next account
			}
			accountsProcessed++
		}
	}

	l.Printf("Successfully reset quotas for %d accounts.", accountsProcessed)
	return nil
}

// --- RecipePairing Functions ---

// GetRecipePairing retrieves a cached recipe summary and its wine suggestions using a
// unique key (either a URL or a content hash).
func (dl *DataLayer) GetRecipePairing(ctx context.Context, id string) (RecipePairing, error) {
	var pairing RecipePairing
	key, err := attributevalue.MarshalMap(map[string]string{"ID": id})
	if err != nil {
		return pairing, fmt.Errorf("failed to marshal key for GetRecipePairing: %w", err)
	}

	input := &dynamodb.GetItemInput{
		TableName: aws.String("RecipePairings"),
		Key:       key,
	}

	result, err := dl.client.GetItem(ctx, input)
	if err != nil {
		return pairing, fmt.Errorf("failed to get recipe pairing item: %w", err)
	}

	if result.Item == nil {
		return pairing, ErrNotFound
	}

	if err = attributevalue.UnmarshalMap(result.Item, &pairing); err != nil {
		return pairing, fmt.Errorf("failed to unmarshal recipe pairing item: %w", err)
	}

	return pairing, nil
}

// CreateRecipePairing creates or updates a recipe pairing record, storing the summary
// and the list of generated wine suggestions.
func (dl *DataLayer) CreateRecipePairing(ctx context.Context, id string, pairingType PairingType, summary string, suggestions []Suggestion) (RecipePairing, error) {
	l := log.New(log.Default().Writer(), "[CreateRecipePairing]", log.Default().Flags())

	pairing := RecipePairing{
		ID:          id,
		Type:        pairingType,
		DateCreated: time.Now(),
		Summary:     summary,
		Suggestions: suggestions,
	}

	l.Printf("Creating pairing: ID=%s, Type=%s, SuggestionsCount=%d\n", id, pairingType, len(suggestions))

	item, err := attributevalue.MarshalMap(pairing)
	if err != nil {
		return pairing, fmt.Errorf("failed to marshal recipe pairing: %w", err)
	}

	// Log the marshaled Type field to verify it's being stored correctly
	if typeAttr, ok := item["Type"]; ok {
		l.Printf("Marshaled Type attribute: %+v\n", typeAttr)
	}

	input := &dynamodb.PutItemInput{
		TableName: aws.String("RecipePairings"),
		Item:      item,
	}

	_, err = dl.client.PutItem(ctx, input)
	if err != nil {
		l.Printf("Failed to store in DynamoDB: %v\n", err)
		return pairing, fmt.Errorf("failed to create recipe pairing in dynamodb: %w", err)
	}

	l.Printf("Successfully stored pairing with ID=%s\n", id)
	return pairing, nil
}

// DiagnoseRecipePairings scans the RecipePairings table and logs what it finds.
// This is a diagnostic function to help debug issues with GetRecentRecipePairingIDs.
func (dl *DataLayer) DiagnoseRecipePairings(ctx context.Context) error {
	l := log.New(log.Default().Writer(), "[DiagnoseRecipePairings]", log.Default().Flags())

	l.Println("Scanning RecipePairings table...")

	scanInput := &dynamodb.ScanInput{
		TableName: aws.String("RecipePairings"),
		Limit:     aws.Int32(10), // Only scan first 10 items
	}

	result, err := dl.client.Scan(ctx, scanInput)
	if err != nil {
		l.Printf("Scan failed: %v\n", err)
		return fmt.Errorf("failed to scan table: %w", err)
	}

	l.Printf("Found %d items in table\n", len(result.Items))

	for i, item := range result.Items {
		l.Printf("Item %d:\n", i)

		// Check each expected field
		if id, ok := item["ID"]; ok {
			l.Printf("  ID: %+v\n", id)
		} else {
			l.Printf("  ID: MISSING\n")
		}

		if typeAttr, ok := item["Type"]; ok {
			l.Printf("  Type: %+v\n", typeAttr)
		} else {
			l.Printf("  Type: MISSING\n")
		}

		if dateCreated, ok := item["DateCreated"]; ok {
			l.Printf("  DateCreated: %+v\n", dateCreated)
		} else {
			l.Printf("  DateCreated: MISSING\n")
		}

		l.Printf("  All attributes: %+v\n", item)
	}

	return nil
}

// GetRecentRecipePairingIDs retrieves a list of IDs for recently created recipe pairings,
// which can be used to display sample links to users.
func (dl *DataLayer) GetRecentRecipePairingIDs(ctx context.Context, pairingType PairingType, limit int) ([]string, error) {
	l := log.New(log.Default().Writer(), "[GetRecentRecipePairingIDs]", log.Default().Flags())
	var ids []string

	l.Printf("Querying for pairingType=%s, limit=%d\n", pairingType, limit)

	input := &dynamodb.QueryInput{
		TableName:              aws.String("RecipePairings"),
		IndexName:              aws.String("Type-DateCreated-index"),
		KeyConditionExpression: aws.String("#type = :typeVal"),
		ExpressionAttributeNames: map[string]string{
			"#type": "Type",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":typeVal": &types.AttributeValueMemberS{Value: string(pairingType)},
		},
		ScanIndexForward: aws.Bool(false), // Sort by DateCreated descending
		Limit:            aws.Int32(int32(limit)),
	}

	result, err := dl.client.Query(ctx, input)
	if err != nil {
		l.Printf("Query failed: %v\n", err)
		return nil, fmt.Errorf("failed to query for recent recipe pairings: %w", err)
	}

	l.Printf("Query returned %d items\n", len(result.Items))

	for i, item := range result.Items {
		// Since GSI uses ProjectionTypeKeysOnly, we only get ID, Type, and DateCreated
		// Extract ID directly from the item instead of unmarshaling the full struct
		if idAttr, ok := item["ID"]; ok {
			if idVal, ok := idAttr.(*types.AttributeValueMemberS); ok {
				ids = append(ids, idVal.Value)
				l.Printf("Item %d: ID=%s\n", i, idVal.Value)
			} else {
				l.Printf("Item %d: ID attribute is not a string: %T\n", i, idAttr)
			}
		} else {
			l.Printf("Item %d: No ID attribute found in item: %+v\n", i, item)
		}
	}

	// If query returned no results, try a diagnostic scan to see what's in the table
	if len(ids) == 0 {
		l.Printf("Query returned 0 results. Running diagnostic scan...\n")

		scanInput := &dynamodb.ScanInput{
			TableName: aws.String("RecipePairings"),
			Limit:     aws.Int32(5),
		}

		scanResult, scanErr := dl.client.Scan(ctx, scanInput)
		if scanErr != nil {
			l.Printf("Diagnostic scan failed: %v\n", scanErr)
		} else {
			l.Printf("Diagnostic scan found %d total items in table\n", len(scanResult.Items))
			for i, item := range scanResult.Items {
				l.Printf("Scan item %d:\n", i)
				if id, ok := item["ID"]; ok {
					l.Printf("  ID=%+v\n", id)
				}
				if typeAttr, ok := item["Type"]; ok {
					l.Printf("  Type=%+v\n", typeAttr)
				} else {
					l.Printf("  Type=MISSING (this is the problem!)\n")
				}
				if date, ok := item["DateCreated"]; ok {
					l.Printf("  DateCreated=%+v\n", date)
				}
			}
		}
	}

	l.Printf("Returning %d IDs\n", len(ids))
	return ids, nil
}
