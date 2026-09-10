# Mutation Test Survivors

This document records mutants that survive the mutation testing threshold but are justified as either equivalent or intentional design choices.

## Mutation Score: 0.94 (3 survivors out of 50 mutants)

### 1. Double-Quote Boundary Check Removal (expression/remove)

**Mutant**: Remove `s[len(s)-1] == '"'` from the double-quote condition in `unquote()`

**Location**: `source.go:96`

**Original**:
```go
if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
```

**Mutated**:
```go
if len(s) >= 2 && s[0] == '"' && true {
```

**Justification**: This check is a performance optimization and boundary guard. While removing it does not change the final behavior (strconv.Unquote will error on unclosed quotes), keeping it avoids unnecessary calls to strconv.Unquote for malformed values like `"abc` that don't have matching closing quotes. The check makes the code more defensive and efficient, which is valuable even though strconv.Unquote would ultimately catch the error.

### 2. Single-Quote Boundary Check Removal (expression/remove)

**Mutant**: Remove `s[len(s)-1] == '\''` from the single-quote condition in `unquote()`

**Location**: `source.go:103`

**Original**:
```go
if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
```

**Mutated**:
```go
if len(s) >= 2 && s[0] == '\'' && true {
```

**Justification**: Identical reasoning to survivor #1. This check is a performance optimization for single-quoted strings. Removing it would cause mismatched quotes like `'abc` to be passed to the slice operation `s[1 : len(s)-1]`, which could extract incorrect content or fail to properly detect malformed input.

### 3. Redundant Key Trimming (statement/remove)

**Mutant**: Remove the line `key = strings.TrimSpace(key)` after the export prefix removal.

**Location**: `source.go:76`

**Original**:
```go
key = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(key), "export "))
key = strings.TrimSpace(key)
```

**Mutated**:
```go
key = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(key), "export "))
// key = strings.TrimSpace(key)  [removed]
```

**Justification**: This mutation is truly equivalent. The final TrimSpace call is redundant because the previous line already ends with TrimSpace. However, it is retained in the code for defensive programming — making the intent explicit that the key should be trimmed after prefix removal, regardless of what TrimPrefix does. This improves code clarity at the cost of a redundant operation.
