# graphql-go-preprocessor

This is a small preprocessor for graphql-go schema structures. Given a schema, it can systematically remove or modify definitions. It's used primarily via a single function:

```go
func PreprocessSchemaConfig(input graphql.SchemaConfig, config *PreprocessorConfig) graphql.SchemaConfig
```

`PreprocessorConfig` has a single field: `BetaFeaturesEnabled`. If this field is set to false, the preprocessor will intelligently filter out types marked as "beta" by the `Beta` function (and their dependents), making feature toggles trivial to maintain.
