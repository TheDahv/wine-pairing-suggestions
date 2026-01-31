# Recipe Endpoints Data Layer Migration Plan

## Overview
This document outlines the plan to migrate Recipe-related endpoints from cache-only operations to parallel cache + DynamoDB operations, following the patterns established for Account-related endpoints.

## Current State Analysis

### Account Migration Pattern (Already Implemented)
The Account-related operations demonstrate the migration pattern we'll follow:

1. **Dual System Access**: Operations access both cache and DynamoDB in parallel
2. **Structured Logging**: All operations use `[CACHE]` and `[DB]` prefixes to distinguish system calls
3. **Side-by-Side Operation**: Both systems operate independently; failures in one don't block the other
4. **Graceful Degradation**: DynamoDB errors are logged but don't fail requests (cache continues to function)

**Examples from webapp.go:**
- **PostOauthResponse** (lines 752-823): Creates accounts in both cache and DynamoDB
- **WithAccountDetails** (lines 279-339): Fetches account details from both systems
- **WithSufficientQuota** (lines 341-379): Checks quota from both systems
- **GetRecipeWineSuggestions** (lines 688-693): Decrements quota in both systems
- **GetRecipeWineSuggestionsV2** (lines 639-647): Decrements quota in both systems

### Recipe Endpoints to Migrate

#### 1. GetRecipeWineSuggestionsV2 (lines 584-654)
**Current behavior:**
- Uses cache key: `recipes:suggestions-json:{url or content-hash}`
- On cache miss: Generates suggestions using LLM with tools
- On cache hit: Returns cached JSON directly
- Already decrements quota in both systems ✓

**Migration needed:**
- Add parallel DynamoDB get/set for recipe pairings
- Handle both URL-based and content-hash-based pairings
- Store suggestions + summary together in DynamoDB (matches RecipePairing model)
- Parse SuggestionsResponse to extract suggestions array and summary

#### 2. GetRecipeWineSuggestions (lines 661-705)
**Current behavior:**
- Uses cache keys:
  - `recipes:summarized:{url}` - recipe summary (must already exist)
  - `recipes:suggestions-json:{url}` - generated suggestions
- On cache miss for suggestions: Generates using LLM, decrements quota in both systems ✓
- Returns JSON array of suggestions

**Migration needed:**
- Add parallel DynamoDB get/set for recipe pairings
- Only handles URL-based pairings (Type: PairingTypeURL)
- Store suggestions + summary together in DynamoDB
- Parse suggestions array from GeneratePairingSuggestions response

#### 3. GetRecentSuggestions (lines 710-740)
**Current behavior:**
- Uses `cache.GetKeys("recipes:suggestions-json:*")` to scan all cached suggestions
- Extracts URLs from cache keys using regex
- Returns random sample of 3 URLs

**Migration needed:**
- Add parallel DynamoDB query using `GetRecentRecipePairingIDs`
- Query for both PairingTypeURL entries
- Combine results from cache and DynamoDB (deduplicate)
- Return union of recent suggestions from both systems

#### 4. PostCreateRecipe (lines 484-565)
**Current behavior:**
- Caches raw HTML: `recipes:raw:{url}`
- Caches parsed markdown: `recipes:parsed:{url}`
- Caches summary: `recipes:summarized:{url}`
- Returns summary JSON

**Migration decision:**
- **No DynamoDB migration needed** for raw/parsed content (out of scope for RecipePairing model)
- The summary will be stored in DynamoDB as part of the RecipePairing when suggestions are generated
- This endpoint remains cache-only for now

## Data Model Alignment

### Cache Structure
```
recipes:raw:{url}              → Raw HTML string
recipes:parsed:{url}           → Markdown string
recipes:summarized:{url}       → Summary string
recipes:suggestions-json:{url} → JSON array of suggestions
recipes:suggestions-json:content:{hash} → JSON array of suggestions (for content-hash)
```

### DynamoDB Structure (RecipePairing)
```go
type RecipePairing struct {
    ID          string       // URL or content hash
    Type        PairingType  // "URL" or "ContentHash"
    DateCreated time.Time
    Summary     string       // Recipe summary
    Suggestions []Suggestion // Wine pairing suggestions
}

type Suggestion struct {
    Style       string
    Region      string
    Description string
    PairingNote string
}
```

### Key Differences
1. **Cache stores summary and suggestions separately**; DynamoDB stores them together
2. **Cache has no concept of PairingType**; DynamoDB distinguishes URL vs ContentHash
3. **Cache doesn't track DateCreated**; DynamoDB does for recent queries
4. **Cache stores raw/parsed content**; DynamoDB only stores final pairings

## Migration Strategy

### Pattern to Follow

```go
func (wa *Webapp) SomeRecipeEndpoint(w http.ResponseWriter, r *http.Request) {
    ctx := context.Background()
    l := log.New(log.Default().Writer(), "[EndpointName]", log.Default().Flags())

    // 1. Try cache first (fast path)
    l.Println("[CACHE] Checking cache for key:", cacheKey)
    if cached, err := wa.cache.Get(cacheKey); err == nil {
        l.Println("[CACHE] Cache hit, returning cached result")
        // Return cached result
        return
    }
    l.Println("[CACHE] Cache miss")

    // 2. Try DynamoDB (persistent storage)
    l.Printf("[DB] Checking DynamoDB for pairing ID: %s\n", pairingID)
    if pairing, err := wa.dl.GetRecipePairing(ctx, pairingID); err == nil {
        l.Printf("[DB] Found pairing in DynamoDB (created: %s)\n", pairing.DateCreated)

        // Reconstruct response from pairing
        // Store in cache for future fast access
        l.Println("[CACHE] Backfilling cache from DynamoDB result")
        wa.cache.Set(cacheKey, reconstructedJSON)

        // Return result
        return
    } else if !errors.Is(err, data.ErrNotFound) {
        l.Printf("[DB] Error querying DynamoDB: %v\n", err)
    } else {
        l.Println("[DB] Pairing not found in DynamoDB")
    }

    // 3. Generate new content (both systems missed)
    l.Println("Generating new content...")
    result := generateContent()

    // 4. Store in both systems
    l.Println("[CACHE] Storing in cache")
    if err := wa.cache.Set(cacheKey, result); err != nil {
        l.Printf("[CACHE] Error storing in cache: %v\n", err)
    }

    l.Println("[DB] Storing in DynamoDB")
    if _, err := wa.dl.CreateRecipePairing(ctx, pairingID, pairingType, summary, suggestions); err != nil {
        l.Printf("[DB] Error storing in DynamoDB: %v\n", err)
    }

    // Return result
}
```

### Helper Functions Needed

```go
// Determine pairing type from input
func getPairingTypeForInput(input string) (id string, pairingType data.PairingType) {
    if matches := recentSuggestionRx.FindString(input); matches != "" {
        return matches, data.PairingTypeURL
    }
    hash := helpers.HashContent(input)
    return hash, data.PairingTypeContentHash
}

// Reconstruct JSON from RecipePairing for cache compatibility
func reconstructSuggestionsJSON(suggestions []data.Suggestion) string {
    // Convert data.Suggestion to models.Suggestion format
    // Marshal to JSON
}

// Parse suggestions response and store in both systems
func storePairingInBothSystems(ctx context.Context, wa *Webapp, id string, pairingType data.PairingType,
    cacheKey string, suggestionsJSON string, summary string, suggestions []data.Suggestion) {
    // Store in cache
    // Store in DynamoDB
}
```

## Implementation Steps

### Step 1: Add Helper Functions
Create helper functions in webapp.go to:
- Determine pairing type from input (URL vs content hash)
- Reconstruct JSON responses from RecipePairing structs
- Convert between models.Suggestion and data.Suggestion types

### Step 2: Migrate GetRecipeWineSuggestionsV2
1. Add logger with `[CACHE]` and `[DB]` prefixes
2. Keep cache check as fast path (existing logic)
3. On cache miss, check DynamoDB using `GetRecipePairing`
4. If found in DynamoDB:
   - Reconstruct suggestions JSON response
   - Backfill cache with JSON
   - Return result
5. If not in DynamoDB, generate using existing LLM logic
6. Parse `SuggestionsResponse` to extract suggestions array and summary
7. Store in both cache (JSON) and DynamoDB (structured)
8. Handle both URL and content-hash based keys

### Step 3: Migrate GetRecipeWineSuggestions
1. Add logger with `[CACHE]` and `[DB]` prefixes
2. Keep cache check for summary (existing logic)
3. For suggestions:
   - Try cache first (fast path)
   - On cache miss, try DynamoDB using `GetRecipePairing`
   - If found in DynamoDB, reconstruct and backfill cache
   - If not found, generate using existing LLM logic
4. Parse suggestions array from LLM response
5. Store in both cache (JSON) and DynamoDB (structured)
6. Only handles PairingTypeURL

### Step 4: Migrate GetRecentSuggestions
1. Add logger with `[CACHE]` and `[DB]` prefixes
2. Query cache using existing `GetKeys` logic
3. Parallelize with DynamoDB query using `GetRecentRecipePairingIDs`:
   - Query for `PairingTypeURL` with limit
4. Combine and deduplicate results (union of both sources)
5. Return random sample

### Step 5: Testing
1. Test cache hit path (should be unchanged)
2. Test DynamoDB hit + cache miss (should backfill cache)
3. Test both systems miss (should generate and store in both)
4. Verify logging shows parallel operations
5. Verify DynamoDB errors don't fail requests
6. Test URL-based and content-hash-based pairings

## Logging Conventions

All logs should follow the Account migration pattern:
- Use `log.New()` with descriptive prefix (e.g., `[GetRecipeWineSuggestionsV2]`)
- Prefix cache operations with `[CACHE]`
- Prefix DynamoDB operations with `[DB]`
- Log key information: cache keys, pairing IDs, errors, successes
- Log errors but continue execution (graceful degradation)

Example:
```go
l := log.New(log.Default().Writer(), "[GetRecipeWineSuggestionsV2]", log.Default().Flags())
l.Println("[CACHE] Checking cache for key:", cacheKey)
l.Println("[DB] Fetching pairing from DynamoDB, ID:", pairingID)
l.Printf("[DB] Error fetching from DynamoDB: %v\n", err)
```

## Error Handling

Follow the Account pattern:
- **Cache errors**: Log and continue (try DynamoDB)
- **DynamoDB errors**: Log but don't fail request (cache can still function)
- **Generation errors**: Return error to user (both systems missed and can't generate)
- **Storage errors**: Log but don't fail request (data was generated successfully)

## Backward Compatibility

- All cache keys remain unchanged
- Cache remains the fast path for performance
- Existing cached data continues to work
- New data is stored in both systems
- Over time, DynamoDB becomes populated through normal usage
- No migration of existing cache data required

## Success Criteria

1. ✅ Recipe endpoints use both cache and DynamoDB in parallel
2. ✅ Logging clearly distinguishes cache vs DynamoDB operations
3. ✅ Cache remains the fast path (performance not degraded)
4. ✅ DynamoDB errors don't fail requests
5. ✅ Both URL-based and content-hash-based pairings work
6. ✅ Recent suggestions query combines results from both systems
7. ✅ Code follows same patterns as Account migration

## Out of Scope

- Migration of existing cache data to DynamoDB (will populate organically)
- Storing raw HTML or parsed markdown in DynamoDB (not part of RecipePairing model)
- Removing cache dependencies (parallel operation is the goal)
- Performance optimization beyond maintaining cache as fast path

## Cache Gating Implementation (COMPLETED)

### Overview
All cache operations are now gated behind the `ENABLE_CACHE` environment variable, making DynamoDB the primary data source with cache as an optional performance layer. This is a critical step toward deprecating the cache layer entirely.

### Environment Variable
- **Variable**: `ENABLE_CACHE`
- **Values**: `"true"` to enable cache, any other value (or unset) disables it
- **Default**: Disabled (cache not used)

### Operational Modes

#### Cache Disabled (Default - Production Ready)
When `ENABLE_CACHE` is not set to `"true"`:
- **DynamoDB is the sole data source**
- No cache health check at startup
- All cache read operations are skipped
- All cache write operations are skipped
- HealthStatus checks only DynamoDB connectivity
- Full functionality maintained through DynamoDB

#### Cache Enabled (Transitional)
When `ENABLE_CACHE=true`:
- **DynamoDB is primary, cache is performance layer**
- Cache health check runs at startup
- Cache checked after DB (for faster subsequent reads)
- Cache backfilled from DB when empty
- Both systems updated on writes
- Provides migration path for existing deployments

### Implementation Details

#### Webapp Initialization
```go
// In Start() method:
wa.cacheEnabled = os.Getenv("ENABLE_CACHE") == "true"

if wa.cacheEnabled {
    // Check cache health
} else {
    // Skip cache check, use DB only
}
```

#### Operation Pattern (All Endpoints)
```go
// 1. PRIMARY: Try DynamoDB first
data, err := wa.dl.GetData(ctx, id)
if err == nil {
    // Found in DB
    if wa.cacheEnabled {
        // Backfill cache for future performance
        wa.cache.Set(key, data)
    }
    return data
}

// 2. OPTIONAL: Try cache if enabled and DB missed
if wa.cacheEnabled {
    if cached, err := wa.cache.Get(key); err == nil {
        return cached
    }
}

// 3. Generate new if both missed
newData := generate()

// 4. Store in DB (always)
wa.dl.Save(ctx, id, newData)

// 5. Store in cache (if enabled)
if wa.cacheEnabled {
    wa.cache.Set(key, newData)
}
```

### Affected Endpoints

#### Account Operations
- **WithAccountDetails**: Fetches from DB first, cache only if enabled
- **WithSufficientQuota**: Uses DB quota, cache only if enabled
- **PostOauthResponse**: Creates in DB first, cache only if enabled
- **DeleteSession**: Deletes from cache only if enabled

#### Recipe Operations
- **GetRecipeWineSuggestionsV2**: Checks DB first, cache only if enabled
- **GetRecipeWineSuggestions**: Checks DB first, cache only if enabled (requires cache or prior DB pairing for summary)
- **GetRecentSuggestions**: Queries DB first, combines with cache if enabled
- **PostCreateRecipe**: Uses cache if enabled, processes fresh if disabled (no DB storage)

### Migration Benefits

1. **Zero-Downtime Testing**: Can test DynamoDB-only operation without code changes
2. **Gradual Rollout**: Can enable cache in some environments while testing DB-only in others
3. **Easy Rollback**: Toggle environment variable to revert to cache behavior
4. **Clear Path Forward**: Sets foundation for complete cache removal
5. **Cost Optimization**: Can disable cache in environments where it's not needed

### Performance Considerations

**Cache Disabled**:
- All operations hit DynamoDB (slower but consistent)
- No Redis/cache infrastructure required
- Suitable for low-traffic or testing environments

**Cache Enabled**:
- First request hits DynamoDB (source of truth)
- Subsequent requests use cache (faster)
- Cache is backfilled from DynamoDB automatically
- Best performance for high-traffic environments

### Deployment Strategy

**Phase 1 (Current)**: 
- Deploy with `ENABLE_CACHE=true` (default transitional mode)
- Both systems operating, DB primary

**Phase 2 (Validation)**:
- Deploy to staging with `ENABLE_CACHE` unset
- Validate DynamoDB-only operation
- Monitor performance and errors

**Phase 3 (Production Cutover)**:
- Gradually disable cache in production environments
- Monitor performance metrics
- Keep cache infrastructure for quick rollback if needed

**Phase 4 (Cleanup)**:
- After validation period, remove cache infrastructure
- Remove cache code from codebase
- Simplify to single data source

### Testing Checklist

- [x] Code compiles with cache flag
- [x] Application starts with cache disabled
- [x] Application starts with cache enabled
- [ ] Account operations work with cache disabled
- [ ] Account operations work with cache enabled
- [ ] Recipe operations work with cache disabled
- [ ] Recipe operations work with cache enabled
- [ ] HealthStatus works with cache disabled
- [ ] HealthStatus works with cache enabled
- [ ] Quota management works with cache disabled
- [ ] Quota management works with cache enabled

### Known Limitations

1. **GetRecipeWineSuggestions**: Requires cache enabled OR prior pairing in DB for summary
   - This endpoint has a stateful dependency on PostCreateRecipe
   - Without cache, it needs the pairing to already exist in DB
   - Consider deprecating this endpoint in favor of GetRecipeWineSuggestionsV2

2. **PostCreateRecipe**: Does not store in DynamoDB
   - Raw and parsed content not stored in DB (by design)
   - Without cache, fetches and processes fresh every time
   - Consider adding RecipeContent model if performance is an issue

### Future Work

1. Remove `GetRecipeWineSuggestions` endpoint (superseded by V2)
2. Consider adding RecipeContent table for raw/parsed storage if needed
3. Remove cache client and infrastructure after validation
4. Remove `cacheEnabled` flag and all cache code
5. Simplify to single DynamoDB data source
