# Wine Pairing Suggestions - Codebase Guide

**Purpose**: This guide helps LLM assistants quickly understand the codebase architecture, design patterns, and current migration state to provide efficient, cost-effective, and consistent assistance.

**Last Updated**: 2026-01-26
**Project State**: Production-ready DynamoDB implementation with optional cache layer

---

## Table of Contents
1. [Quick Start for LLM Sessions](#quick-start-for-llm-sessions)
2. [Project Overview](#project-overview)
3. [Architecture & Design Patterns](#architecture--design-patterns)
4. [Module Guide](#module-guide)
5. [Migration Status](#migration-status)
6. [Common Patterns](#common-patterns)
7. [Performance Considerations](#performance-considerations)
8. [Pitfalls to Avoid](#pitfalls-to-avoid)
9. [Testing Strategy](#testing-strategy)
10. [Future Improvements](#future-improvements)

---

## Quick Start for LLM Sessions

**New to this codebase?** Read `AGENTS.md` first for project layout, deployment commands, and AWS/SAM operations.

**Use this guide** for architectural understanding, code patterns, and complex refactoring work.

### First Actions
1. **Read `AGENTS.md`** - Get oriented with project structure and commands
2. **Check git status** - Understand current changes
3. **Check `specs/migrate-data-layer.md`** - Understand migration state
4. **Build the project** - Verify it compiles: `go build ./...`
5. **Return here** - For design patterns and code examples

### Where to Look for What
- **HTTP handlers**: `webapp/webapp.go` (~1200 lines)
- **Data layer**: `data/data.go` (DynamoDB operations)
- **Cache layer**: `cache/*.go` (Redis/Memory - being deprecated)
- **LLM models**: `models/models.go` (Claude/Bedrock integration)
- **Business logic**: Handlers in `webapp/webapp.go`
- **Specs & plans**: `specs/` directory

### Key Files by Importance
1. `webapp/webapp.go` - Core application logic, all handlers
2. `data/data.go` - DynamoDB data layer (source of truth)
3. `models/models.go` - LLM prompt logic
4. `cache/*.go` - Optional performance layer (gated by feature flag)

---

## Project Overview

### What This Application Does
Generates wine pairing suggestions for recipes using LLM (Large Language Model) analysis:
1. User provides a recipe URL or text
2. System fetches/parses recipe content
3. LLM summarizes flavor profiles
4. LLM generates 4-10 wine pairing suggestions
5. Results cached/stored for future quick access

### Tech Stack
- **Language**: Go 1.22+
- **Web Framework**: Native `net/http` (no frameworks)
- **Primary Database**: AWS DynamoDB (local: DynamoDB Local on port 8000)
- **Cache Layer**: Redis or in-memory (optional, gated by `ENABLE_CACHE`)
- **LLM Provider**: Anthropic Claude (via API or AWS Bedrock)
- **Authentication**: Google OAuth (JWT)
- **Deployment**: AWS Lambda + API Gateway (serverless)

### Deployment Modes
1. **Local Development**: Native Go server (port 8080)
2. **AWS Lambda**: Serverless deployment via SAM (see `AGENTS.md` for deployment commands)
3. **Docker**: Local stack with DynamoDB Local + Redis

---

## Architecture & Design Patterns

### Overall Design Philosophy
**Principle**: "DynamoDB is truth, cache is speed"
- DynamoDB = Source of truth, always checked first
- Cache = Optional performance layer, never authoritative
- All operations work without cache enabled
- Cache can be removed in the future with minimal code changes

### Data Flow Pattern

```
Request
  ↓
[Middleware: Auth, Quota Check]
  ↓
[Handler: Check DynamoDB first]
  ↓
[If found in DB] → [Optional: Backfill cache] → Response
  ↓
[If not in DB] → [Optional: Check cache]
  ↓
[If in cache] → Response
  ↓
[Generate with LLM]
  ↓
[Store in DynamoDB (always)]
  ↓
[Store in cache (if enabled)]
  ↓
Response
```

### Key Architectural Decisions

#### 1. Monolithic Handler File
**Decision**: All HTTP handlers in single `webapp/webapp.go` file (~1200 lines)

**Rationale**:
- Simple codebase (no framework overhead)
- Easy to find all endpoints
- Minimal abstraction layers
- Works well for ~10 endpoints

**Trade-off**: File is large but logically organized

#### 2. DynamoDB as Primary Storage
**Decision**: Migrate from cache-first to DynamoDB-first architecture

**Rationale**:
- Persistent storage (survives restarts)
- No cache warming needed
- Consistent data across deployments
- Clear source of truth
- Scales with AWS infrastructure

**Implementation**: See `specs/migrate-data-layer.md` for details

#### 3. Feature Flag for Cache
**Decision**: Gate all cache operations behind `ENABLE_CACHE` environment variable

**Rationale**:
- Enables testing DynamoDB-only mode
- Smooth migration path
- Easy rollback capability
- Clear deprecation path
- Zero-downtime testing

**Usage**:
```bash
# Cache disabled (default, production-ready)
ENABLE_CACHE=""  # or unset

# Cache enabled (transitional)
ENABLE_CACHE=true
```

#### 4. Structured Logging
**Decision**: Use prefixed loggers with `[CACHE]` and `[DB]` tags

**Pattern**:
```go
l := log.New(log.Default().Writer(), "[HandlerName]", log.Default().Flags())
l.Println("[DB] Checking DynamoDB...")
l.Println("[CACHE] Cache enabled - checking cache...")
```

**Rationale**:
- Easy to grep logs by component
- Clear data flow in logs
- Simple debugging
- No logging framework dependency

#### 5. No Over-Engineering
**Decision**: Minimal abstractions, direct implementations

**Examples**:
- No repository pattern (data layer is simple)
- No dependency injection framework
- No middleware library
- Direct struct passing

**Rationale**:
- Faster development
- Easier to understand
- Less code to maintain
- Go idioms over patterns

---

## Module Guide

### `webapp/webapp.go` - HTTP Handlers & Core Logic

**Size**: ~1200 lines
**Purpose**: HTTP server, handlers, middleware, business logic

#### Key Components

**1. Webapp Struct**
```go
type Webapp struct {
    port           int
    tmpl           *template.Template
    cache          cache.Cacher
    cacheEnabled   bool              // Feature flag
    dl             *data.DataLayer   // Primary data source
    googleClientID string
    hostname       string
    model          llms.Model         // LLM client
    toolserver     *mcpserver.MCPServer
    toolclient     *mcpclient.Client
    tools          []tools.Tool
}
```

**2. Middleware Functions**
- `WithSessionRequired`: Validates session cookie, extracts account ID
- `WithAccountDetails`: Loads account from DB (then cache if enabled)
- `WithSufficientQuota`: Checks if user has remaining quota
- Pattern: Middleware wraps handlers, adds context values

**3. Account Endpoints**
- `PostOauthResponse`: Google OAuth callback, creates account
- `DeleteSession`: Logout, clears session
- `GetUserDetails`: Returns current user info

**4. Recipe Endpoints**
- `PostCreateRecipe`: Fetch & summarize recipe (cache-only storage)
- `GetRecipeWineSuggestions`: V1 - URL-based, requires prior summary
- `GetRecipeWineSuggestionsV2`: V2 - URL or text, self-contained
- `GetRecentSuggestions`: List recent pairings from DB + cache

#### Handler Pattern to Follow

```go
func (wa *Webapp) HandlerName(w http.ResponseWriter, r *http.Request) {
    ctx := context.Background()
    l := log.New(log.Default().Writer(), "[HandlerName]", log.Default().Flags())

    // 1. Extract and validate input
    input := getPathValue(r, "param")
    if input == "" {
        helpers.SendJSONError(w, fmt.Errorf("param required"), http.StatusBadRequest)
        return
    }

    // 2. PRIMARY: Check DynamoDB
    l.Println("[DB] Checking DynamoDB...")
    if data, err := wa.dl.GetData(ctx, input); err == nil {
        l.Println("[DB] Found in database")

        // Optionally backfill cache
        if wa.cacheEnabled {
            l.Println("[CACHE] Backfilling cache...")
            wa.cache.Set(key, data)
        }

        w.Header().Add("Content-Type", "application/json")
        fmt.Fprint(w, data)
        return
    }

    // 3. OPTIONAL: Check cache if enabled
    if wa.cacheEnabled {
        l.Println("[CACHE] Checking cache...")
        if cached, err := wa.cache.Get(key); err == nil {
            l.Println("[CACHE] Found in cache")
            w.Header().Add("Content-Type", "application/json")
            fmt.Fprint(w, cached)
            return
        }
    }

    // 4. Generate new data
    l.Println("Generating new data...")
    newData := generate()

    // 5. Store in DB (always)
    l.Println("[DB] Storing in DynamoDB...")
    wa.dl.SaveData(ctx, input, newData)

    // 6. Store in cache (if enabled)
    if wa.cacheEnabled {
        l.Println("[CACHE] Storing in cache...")
        wa.cache.Set(key, newData)
    }

    w.Header().Add("Content-Type", "application/json")
    fmt.Fprint(w, newData)
}
```

#### Important Handler Notes

**GetRecipeWineSuggestionsV2** (recommended):
- Self-contained, handles both URLs and text
- Uses LLM tools (MCP server) for fetching/parsing
- Stores in DynamoDB with both summary and suggestions
- Primary endpoint going forward

**GetRecipeWineSuggestions** (legacy):
- Depends on `PostCreateRecipe` being called first
- Requires summary in cache or prior DB entry
- Consider deprecating in favor of V2
- Still works but has stateful dependency

**PostCreateRecipe**:
- Does NOT store in DynamoDB (by design)
- Stores raw/parsed/summarized in cache if enabled
- Without cache: fetches and processes fresh each time
- Consider adding RecipeContent table if needed

### `data/data.go` - DynamoDB Data Layer

**Size**: ~470 lines
**Purpose**: All DynamoDB operations, source of truth

#### Data Models

```go
type Account struct {
    ID    string `dynamodbav:"ID"`    // Google 'sub' claim
    Email string `dynamodbav:"Email"`
    Quota int    `dynamodbav:"Quota"` // Weekly suggestion limit
}

type RecipePairing struct {
    ID          string       `dynamodbav:"ID"`          // URL or content hash
    Type        PairingType  `dynamodbav:"Type"`        // "URL" or "ContentHash"
    DateCreated time.Time    `dynamodbav:"DateCreated"`
    Summary     string       `dynamodbav:"Summary"`     // Recipe flavor profile
    Suggestions []Suggestion `dynamodbav:"Suggestions"` // Wine pairings
}

type Suggestion struct {
    Style       string `dynamodbav:"Style"`       // Wine style (e.g., "Chardonnay")
    Region      string `dynamodbav:"Region"`      // Wine region
    Description string `dynamodbav:"Description"` // About the wine
    PairingNote string `dynamodbav:"PairingNote"` // Why it pairs well
}

const (
    PairingTypeURL         PairingType = "URL"
    PairingTypeContentHash PairingType = "ContentHash"
)
```

#### DynamoDB Tables

**Accounts Table**:
- **Key**: `ID` (partition key) - Google OAuth 'sub' claim
- **Attributes**: Email, Quota
- **Purpose**: User accounts and quota tracking

**RecipePairings Table**:
- **Key**: `ID` (partition key) - URL or SHA256 hash of content
- **Attributes**: Type, DateCreated, Summary, Suggestions[]
- **GSI**: `Type-DateCreated-index` (Type=partition, DateCreated=sort)
- **Purpose**: Store wine pairing suggestions for recipes

#### Key Functions

**Account Operations**:
- `GetAccountByID`: Retrieve account (returns `ErrNotFound` if missing)
- `CreateAccount`: Create account with default quota (idempotent)
- `DecrementAccountQuota`: Decrease quota by 1 (atomic operation)
- `ResetAllAccountQuotas`: Weekly cron job to restore quotas

**RecipePairing Operations**:
- `GetRecipePairing`: Retrieve by ID (URL or hash)
- `CreateRecipePairing`: Store pairing (creates or overwrites)
- `GetRecentRecipePairingIDs`: Query GSI for recent URLs

**Setup**:
- `SetupTables`: Creates missing tables and GSIs on startup
- Checks existing tables, adds missing GSIs to existing tables
- Idempotent, safe to call on every startup

#### Important Notes

**GSI Creation**:
- GSI (`Type-DateCreated-index`) required for `GetRecentRecipePairingIDs`
- SetupTables automatically adds missing GSIs
- GSI takes 10-30 seconds to become ACTIVE after creation
- Query returns 0 results while GSI is still CREATING

**Error Handling**:
- `ErrNotFound` indicates item doesn't exist (not an error)
- Use `errors.Is(err, data.ErrNotFound)` to check
- All other errors should be logged but not fail requests

**Logging**:
- All functions log operations with `[FunctionName]` prefix
- Diagnostic logging helps debug issues
- Direct ID extraction (not unmarshal) for GSI queries

### `models/models.go` - LLM Integration

**Size**: ~350 lines
**Purpose**: LLM prompts, Claude/Bedrock clients, parsing

#### LLM Providers

**Claude via Anthropic API**:
```go
func MakeClaude(ctx context.Context) (llms.Model, error)
// Requires: ANTHROPIC_API_KEY env var
// Model: claude-3-5-haiku-latest
```

**Claude via AWS Bedrock**:
```go
func MakeBedrockModel(ctx context.Context) (llms.Model, error)
// Uses AWS credentials from environment
// Model: anthropic.claude-3-5-haiku-20241022-v1:0
```

#### Key Functions

**SummarizeRecipe**:
- Input: Markdown representation of recipe
- Output: JSON summary of flavor profile
- Prompt focuses on: flavors, cooking methods, ingredients, weight
- Can abort if content isn't a recipe

**GeneratePairingSuggestions** (V1):
- Input: Recipe summary text
- Output: JSON array of 5-10 wine suggestions
- Simple prompt, no tools

**GeneratePairingSuggestionsV2** (V2):
- Input: URL or recipe text
- Output: JSON with suggestions + summary
- Uses MCP tools for fetching/parsing
- Self-contained, handles entire flow
- Preferred for new implementations

#### Response Models

```go
type Summary struct {
    Ok          bool   `json:"ok"`
    Summary     string `json:"summary"`
    AbortReason string `json:"abortReason"`
}

type Suggestion struct {
    Style       string `json:"style"`
    Region      string `json:"region"`
    Description string `json:"description"`
    PairingNote string `json:"pairingNote"`
}

type SuggestionsResponse struct {
    Suggestions []Suggestion `json:"suggestions"`
    Summary     string       `json:"summary"`
    ErrorMsg    string       `json:"error,omitempty"`
}
```

#### Prompt Engineering Notes

**Principles**:
- Clear, specific instructions
- Request exact JSON format
- Include examples of desired output
- Allow model to abort if content invalid
- Focus on wine shop accessibility

**Cost Optimization**:
- Use Haiku (cheapest) for all operations
- Single-pass generation in V2 (fewer API calls)
- Cache results aggressively

### `cache/*.go` - Cache Layer (Deprecated)

**Purpose**: Optional performance layer, being phased out

**Implementations**:
- `cache/memory.go`: In-memory cache (for testing)
- `cache/redis.go`: Redis client (for production)

**Interface**:
```go
type Cacher interface {
    Get(key string) (string, error)
    Set(key string, val string) error
    SetEx(key string, val string, seconds int) error
    SetNx(key string, val string, seconds int) error
    Decr(key string) error
    Delete(key string) error
    GetKeys(pattern string) ([]string, error)
    GetOrFetch(key string, fetch func() (string, error)) (string, error)
    Check() (bool, error)
}
```

**Current State**:
- All operations gated by `wa.cacheEnabled`
- Default: disabled
- Enable with `ENABLE_CACHE=true`
- Plan: Remove entirely after validation

**Don't Extend**: Focus development on DynamoDB, not cache

### Helper Packages

**`helpers/` package**:
- `FetchRawFromURL`: HTTP client for recipe URLs
- `CreateMarkdownFromRaw`: HTML to Markdown conversion
- `SendJSONError`: Standard JSON error responses
- `GetGoogleJWTToken`: Google OAuth JWT validation
- `HashContent`: SHA256 hash for content-based IDs

**`lambdahelpers/` package**:
- Lambda-specific adaptations
- Path parameter extraction for Lambda runtime
- Bridge between Lambda and standard Go HTTP

---

## Migration Status

### Completed ✅

1. **DynamoDB Data Layer**: Fully implemented
   - Account operations
   - RecipePairing operations
   - GSI for queries
   - Setup automation

2. **Parallel Operation**: All endpoints support both systems
   - DynamoDB checked first
   - Cache used if enabled
   - Logging shows both paths

3. **Feature Flag**: Cache gated behind `ENABLE_CACHE`
   - Default: disabled
   - DynamoDB-only mode works
   - Backwards compatible when enabled

4. **Documentation**: Comprehensive specs
   - Migration plan in `specs/migrate-data-layer.md`
   - Cache gating documented
   - This guide for LLM sessions

### Current State 🟢

**Production Ready**: DynamoDB-only mode fully functional
- All features work without cache
- Performance acceptable for current load
- Logging comprehensive for debugging

**Transitional Mode Available**: Cache can be enabled
- Useful for high-traffic environments
- Provides migration path for existing deployments
- Cache backfilled from DynamoDB automatically

### Next Steps 🔄

1. **Deploy and Monitor**: Run in production with cache disabled
   - Monitor performance metrics
   - Watch for any edge cases
   - Validate quota management

2. **Remove Cache Code** (after validation period):
   - Delete `cache/` package
   - Remove `cacheEnabled` checks
   - Simplify handlers
   - Update tests

3. **Consider Deprecations**:
   - `GetRecipeWineSuggestions` endpoint (use V2)
   - `PostCreateRecipe` as separate endpoint (merge into V2 flow)

---

## Common Patterns

### 1. Context Pattern
```go
// Store in context (middleware)
ctx := context.WithValue(r.Context(), contextKey, value)
next.ServeHTTP(w, r.WithContext(ctx))

// Retrieve in handler
if value, ok := r.Context().Value(contextKey).(Type); ok {
    // use value
}
```

### 2. Error Handling Pattern
```go
// Return JSON errors consistently
if err != nil {
    helpers.SendJSONError(w, fmt.Errorf("operation failed: %v", err), http.StatusInternalServerError)
    return
}

// Check for specific errors
if errors.Is(err, data.ErrNotFound) {
    // Handle not found case
}
```

### 3. Logging Pattern
```go
l := log.New(log.Default().Writer(), "[FunctionName]", log.Default().Flags())
l.Printf("[DB] Operation description: param=%v\n", param)
l.Printf("[CACHE] Cache operation...\n")
```

### 4. Type Conversion Pattern
```go
// Between webapp models and data models
func convertToDataSuggestions(modelSuggestions []models.Suggestion) []data.Suggestion {
    dataSuggestions := make([]data.Suggestion, len(modelSuggestions))
    for i, ms := range modelSuggestions {
        dataSuggestions[i] = data.Suggestion{
            Style:       ms.Style,
            Region:      ms.Region,
            Description: ms.Description,
            PairingNote: ms.PairingNote,
        }
    }
    return dataSuggestions
}
```

### 5. JSON Response Pattern
```go
type ResponseData struct {
    Field1 string `json:"field1"`
    Field2 int    `json:"field2"`
}

data := ResponseData{Field1: "value", Field2: 42}
out, _ := json.Marshal(data)
w.Header().Add("Content-Type", "application/json")
fmt.Fprint(w, string(out))
```

---

## Performance Considerations

### Current Performance Profile

**With Cache Disabled** (Default):
- First request: ~2-3s (LLM generation)
- Subsequent same recipe: ~100-200ms (DynamoDB get)
- Recent suggestions: ~50ms (GSI query)

**With Cache Enabled**:
- First request: ~2-3s (LLM generation)
- Subsequent same recipe: ~10-50ms (Redis get)
- Recent suggestions: ~20ms (Redis scan + DB query)

### Optimization Strategies

**Already Implemented**:
1. Use cheapest LLM (Haiku) for all operations
2. Store complete pairings (avoid re-generation)
3. GSI for efficient recent queries
4. Optional cache layer for hot data

**Available if Needed**:
1. Add RecipeContent table for raw/parsed storage
2. CDN for static assets
3. Lambda provisioned concurrency
4. DynamoDB auto-scaling (already on-demand billing)

**Not Implemented** (YAGNI):
- Background job processing
- Read replicas
- Complex caching strategies
- Pre-computation/warming

### Cost Optimization

**Current Costs** (per 1000 requests):
- LLM (Haiku): ~$0.10 (only on cache/DB miss)
- DynamoDB: ~$0.01 (reads + writes)
- Lambda: ~$0.02 (compute time)
- Total: ~$0.13 per 1000 unique recipes

**Cost Reduction Strategies**:
1. DynamoDB first (avoid redundant LLM calls)
2. Single-pass generation with V2 (fewer API calls)
3. Aggressive result storage
4. On-demand billing (no idle costs)

---

## Pitfalls to Avoid

### 1. Cache as Source of Truth ❌
```go
// WRONG: Check cache first
if cached, err := wa.cache.Get(key); err == nil {
    return cached
}
data := wa.dl.GetData(ctx, key)
```

```go
// CORRECT: Check DB first
if data, err := wa.dl.GetData(ctx, key); err == nil {
    if wa.cacheEnabled {
        wa.cache.Set(key, data)  // Backfill
    }
    return data
}
```

### 2. Ignoring Feature Flag ❌
```go
// WRONG: Assume cache exists
wa.cache.Set(key, value)
```

```go
// CORRECT: Check feature flag
if wa.cacheEnabled {
    wa.cache.Set(key, value)
}
```

### 3. Silent Errors ❌
```go
// WRONG: Ignore errors
data, _ := wa.dl.GetData(ctx, key)
```

```go
// CORRECT: Log and handle
data, err := wa.dl.GetData(ctx, key)
if err != nil {
    l.Printf("[DB] Error getting data: %v\n", err)
    if !errors.Is(err, data.ErrNotFound) {
        // Handle error
    }
}
```

### 4. Missing Context ❌
```go
// WRONG: Global context
ctx := context.Background()
// ... much later ...
wa.dl.GetData(ctx, key)
```

```go
// CORRECT: Use request context
ctx := r.Context()  // In handler
wa.dl.GetData(ctx, key)
```

### 5. Unmarshaling Full Structs from GSI ❌
```go
// WRONG: GSI only projects keys
var pairing RecipePairing
attributevalue.UnmarshalMap(item, &pairing)
// Fields will be empty!
```

```go
// CORRECT: Extract keys directly
if idAttr, ok := item["ID"]; ok {
    if idVal, ok := idAttr.(*types.AttributeValueMemberS); ok {
        id := idVal.Value
    }
}
```

### 6. Creating New Abstractions ❌
```go
// WRONG: Add repository pattern
type RecipeRepository interface {
    Get(id string) Recipe
    Save(recipe Recipe)
}
```

```go
// CORRECT: Use DataLayer directly
wa.dl.GetRecipePairing(ctx, id)
wa.dl.CreateRecipePairing(ctx, id, ...)
```

**Rationale**: Keep it simple, avoid over-engineering

### 7. Forgetting to Build/Test ❌
```go
// After making changes:
go build ./...           // Always check compilation
go test ./...            // Run tests if they exist
make run-webapp          // Test locally before committing
```

---

## Testing Strategy

### Current Testing State
**Limited formal tests**: Focus is on local testing and logging

**Local Testing Setup**:
```bash
# Start dependencies
docker-compose up -d        # Redis + DynamoDB Local

# Run webapp
make run-webapp             # Starts on :8080

# Check database
open http://localhost:8001  # DynamoDB Admin UI
```

### Manual Testing Checklist

**Account Flow**:
1. ✅ Login via Google OAuth
2. ✅ Check quota in user details
3. ✅ Generate suggestion (decrements quota)
4. ✅ Verify quota updated in DB
5. ✅ Logout

**Recipe Flow (V2 - Recommended)**:
1. ✅ POST to `/recipes/suggestionsV2/` with URL
2. ✅ Verify stored in DynamoDB
3. ✅ Request again, should return from DB
4. ✅ Check logs show `[DB] Found in database`

**Recent Suggestions**:
1. ✅ GET `/recipes/suggestions/recent`
2. ✅ Verify returns URLs from DB
3. ✅ Check logs show GSI query

**Cache Disabled Mode**:
1. ✅ Unset `ENABLE_CACHE`
2. ✅ Restart app
3. ✅ Verify all operations work
4. ✅ Check logs don't show `[CACHE]` messages

**Cache Enabled Mode**:
1. ✅ Set `ENABLE_CACHE=true`
2. ✅ Restart app
3. ✅ Verify cache health check passes
4. ✅ Check logs show both `[DB]` and `[CACHE]` messages

### Adding Tests (If Needed)

**Recommended Approach**:
- Table-driven tests for helper functions
- Integration tests for DynamoDB operations
- Mock LLM for handler tests
- Don't test framework code (http, DynamoDB SDK)

**Example Test Structure**:
```go
func TestGetRecipePairing(t *testing.T) {
    // Setup local DynamoDB
    dl := setupTestDataLayer(t)

    // Create test data
    pairing := createTestPairing()
    dl.CreateRecipePairing(ctx, pairing.ID, ...)

    // Test retrieval
    result, err := dl.GetRecipePairing(ctx, pairing.ID)
    assert.NoError(t, err)
    assert.Equal(t, pairing.ID, result.ID)
}
```

---

## Future Improvements

### Short Term (Next Sprint)

1. **Remove Legacy Endpoint**:
   - Deprecate `GetRecipeWineSuggestions`
   - Redirect to V2 in client code
   - Remove after transition period

2. **Add RecipeContent Table** (if needed):
   - Store raw/parsed content
   - Remove dependency on cache for `PostCreateRecipe`
   - Full DynamoDB operation without cache

3. **Remove Cache Code**:
   - After validation period (2-4 weeks)
   - Delete `cache/` package
   - Remove `cacheEnabled` checks
   - Simplify all handlers

### Medium Term (Next Month)

1. **Improve Error Messages**:
   - User-friendly error responses
   - Better validation messages
   - Localization support

2. **Add Admin Endpoints**:
   - Reset user quota manually
   - View system stats
   - Manual cache invalidation (if cache still exists)

3. **Monitoring & Metrics**:
   - Prometheus metrics
   - Request latency tracking
   - Error rate monitoring
   - Quota usage analytics

### Long Term (Next Quarter)

1. **Recipe Scraping Service**:
   - Extract MCP tool into separate service
   - Handle more recipe formats
   - Better error handling for scraping

2. **User Preferences**:
   - Save favorite wine regions
   - Wine preference profiling
   - Custom pairing suggestions

3. **Social Features**:
   - Share pairings
   - Rate suggestions
   - Community recommendations

---

## Quick Reference

### Environment Variables

**See `AGENTS.md` for production deployment variables.**

**Additional development variables:**
```bash
REDIS_HOST=localhost                 # Redis host (legacy, use VALKEY_ENDPOINT)
REDIS_PORT=6379                      # Redis port (legacy, use VALKEY_ENDPOINT)
LOG_LEVEL=TRACE                      # Enable verbose logging
```

### Key Commands

**Deployment/AWS operations:** See `AGENTS.md` for `make deploy`, SAM CLI, and AWS CLI commands.

**Local development:**
```bash
make build-local             # Build native binary
make run-local               # Run local server
make run-docker-bg           # Start Docker stack
go build ./...               # Compile check
go test ./...                # Run tests
```

**Local DynamoDB inspection:**
```bash
aws dynamodb scan --table-name RecipePairings --endpoint-url http://localhost:8000
aws dynamodb describe-table --table-name RecipePairings --endpoint-url http://localhost:8000
```

### Useful Logs to Grep
```bash
# Find DB operations
grep "\[DB\]" logs.txt

# Find cache operations
grep "\[CACHE\]" logs.txt

# Find errors
grep -i "error" logs.txt

# Find specific handler
grep "\[GetRecentSuggestions\]" logs.txt

# Find quota operations
grep "quota" logs.txt
```

### API Endpoints
```
POST   /oauth/response/                # Google OAuth callback
GET    /logout                         # Logout
GET    /user                           # User details

POST   /recipes/summary/{url}          # Summarize recipe (cache-only)
GET    /recipes/suggestions/{url}      # V1 wine suggestions
POST   /recipes/suggestionsV2/         # V2 wine suggestions (recommended)
GET    /recipes/suggestions/recent     # Recent pairings

GET    /healthz                        # Health check
GET    /                               # Home page
```

---

## For Future LLM Sessions

### First-Time Setup
1. Read `AGENTS.md` for project layout and commands
2. Read this file for architecture and patterns
3. Check `specs/migrate-data-layer.md` for migration details
4. Run `git log --oneline -10` to see recent changes
4. Run `git status` to see current state
5. Build: `go build ./...`

### Before Making Changes
1. Understand current state from specs
2. Check if similar pattern exists
3. Follow established patterns
4. Consider DynamoDB-first approach
5. Add structured logging
6. Test locally before committing

### When Stuck
1. Check this guide first
2. Look for similar code in `webapp/webapp.go`
3. Check git history: `git log --grep="keyword"`
4. Review specs in `specs/` directory
5. Ask user about requirements before assuming

### Code Review Checklist
- [ ] Builds without errors: `go build ./...`
- [ ] Follows DynamoDB-first pattern
- [ ] Respects `cacheEnabled` feature flag
- [ ] Includes structured logging with prefixes
- [ ] Handles errors properly (not silently)
- [ ] Uses existing helper functions
- [ ] No over-engineering or new abstractions
- [ ] Consistent with existing code style
- [ ] Commit message is descriptive

### Commit Message Format
```
Short summary (imperative mood)

Detailed explanation:
- What changed
- Why it changed
- Any important notes

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>
```

---

**Remember**: This codebase values simplicity, directness, and maintainability over clever abstractions. When in doubt, follow existing patterns and keep it simple.

**Last Updated**: 2026-01-26
**Status**: Production-ready DynamoDB implementation with optional cache
