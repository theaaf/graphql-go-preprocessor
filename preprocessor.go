package graphqlapi

import (
	"fmt"
	"reflect"
	"runtime/debug"

	"github.com/graphql-go/graphql"
	"github.com/graphql-go/graphql/language/ast"
)

type Conditional struct {
	OfType    graphql.Type
	Suffix    string
	Condition func(*PreprocessorConfig) bool
}

func (b *Conditional) Name() string {
	return b.OfType.Name() + b.Suffix
}

func (b *Conditional) Description() string {
	return b.OfType.Description()
}

func (b *Conditional) String() string {
	return b.OfType.String() + b.Suffix
}

func (b *Conditional) Error() error {
	return b.OfType.Error()
}

func Beta(ofType graphql.Type) *Conditional {
	return &Conditional{
		OfType: ofType,
		Suffix: "β",
		Condition: func(cfg *PreprocessorConfig) bool {
			return cfg.BetaFeaturesEnabled
		},
	}
}

func BetaEnum(value *graphql.EnumValueConfig) *graphql.EnumValueConfig {
	return &graphql.EnumValueConfig{
		Value: &conditionalEnum{
			Value: value,
			Condition: func(cfg *PreprocessorConfig) bool {
				return cfg.BetaFeaturesEnabled
			},
		},
	}
}

type conditionalEnum struct {
	Value     *graphql.EnumValueConfig
	Condition func(*PreprocessorConfig) bool
}

type PreprocessorConfig struct {
	BetaFeaturesEnabled bool
}

type preprocessor struct {
	Config            *PreprocessorConfig
	PreprocessedTypes map[string]graphql.Type
}

func PreprocessSchemaConfig(input graphql.SchemaConfig, config *PreprocessorConfig) graphql.SchemaConfig {
	p := &preprocessor{
		Config:            config,
		PreprocessedTypes: make(map[string]graphql.Type),
	}
	result := input
	if obj := input.Query; obj != nil {
		result.Query = p.preprocessObject(obj)
	}
	if obj := input.Mutation; obj != nil {
		result.Mutation = p.preprocessObject(obj)
	}
	if obj := input.Subscription; obj != nil {
		result.Subscription = p.preprocessObject(obj)
	}
	result.Types = nil
	for _, t := range input.Types {
		if newType, ok := p.preprocessType(t); ok {
			result.Types = append(result.Types, newType)
		}
	}
	return result
}

// Workaround for https://github.com/graphql-go/graphql/issues/250
var fixedDateTime = graphql.NewScalar(graphql.ScalarConfig{
	Name:        graphql.DateTime.Name(),
	Description: graphql.DateTime.Description(),
	Serialize:   graphql.DateTime.Serialize,
	ParseValue:  graphql.DateTime.ParseValue,
	ParseLiteral: func(valueAST ast.Value) interface{} {
		switch valueAST := valueAST.(type) {
		case *ast.StringValue:
			return graphql.DateTime.ParseValue(valueAST.Value)
		}
		return nil
	},
})

func (p *preprocessor) preprocessType(t graphql.Type) (result graphql.Type, ok bool) {
	if result, ok := p.PreprocessedTypes[t.String()]; ok {
		return result, result != nil
	}
	defer func() {
		p.PreprocessedTypes[t.String()] = result
	}()

	switch t := t.(type) {
	case *graphql.List:
		ofType, ok := p.preprocessType(t.OfType)
		if !ok {
			return nil, false
		}
		return graphql.NewList(ofType), true
	case *graphql.NonNull:
		ofType, ok := p.preprocessType(t.OfType)
		if !ok {
			return nil, false
		}
		return graphql.NewNonNull(ofType), true
	case *graphql.InputObject:
		return p.preprocessInputObject(t), true
	case *graphql.Object:
		return p.preprocessObject(t), true
	case *Conditional:
		if t.Condition(p.Config) {
			return p.preprocessType(t.OfType)
		}
		return nil, false
	case *graphql.Scalar:
		if t.Name() == "DateTime" {
			return fixedDateTime, true
		}
		return t, true
	case *graphql.Enum:
		return p.preprocessEnum(t), true
	case *graphql.Interface:
		return p.preprocessInterface(t), true
	case *graphql.Union:
		return p.preprocessUnion(t), true
	}

	panic(fmt.Errorf("unknown graphql type %T", t))
}

func resolveWrapper(resolve graphql.FieldResolveFn) graphql.FieldResolveFn {
	if resolve == nil {
		return nil
	}
	return func(p graphql.ResolveParams) (v interface{}, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%v\n%v", r, string(debug.Stack()))
			}
		}()

		v, err = resolve(p)

		// graphql-go interprets typed nil as non-null. that makes things messy and error-prone, so
		// let's just fix that for all our resolve functions here
		if v != nil {
			if vValue := reflect.ValueOf(v); vValue.Kind() == reflect.Ptr && vValue.IsNil() {
				v = nil
			}
		}

		return v, err
	}
}

func (p *preprocessor) preprocessEnum(enum *graphql.Enum) *graphql.Enum {
	config := graphql.EnumConfig{
		Name:        enum.Name(),
		Description: enum.Description(),
		Values:      make(map[string]*graphql.EnumValueConfig),
	}
	for _, value := range enum.Values() {
		if Conditional, ok := value.Value.(*conditionalEnum); ok {
			if Conditional.Condition(p.Config) {
				config.Values[value.Name] = Conditional.Value
			}
		} else {
			config.Values[value.Name] = &graphql.EnumValueConfig{
				Value:             value.Value,
				Description:       value.Description,
				DeprecationReason: value.DeprecationReason,
			}
		}
	}
	return graphql.NewEnum(config)
}

func (p *preprocessor) preprocessField(def *graphql.FieldDefinition) (*graphql.Field, bool) {
	newType, ok := p.preprocessType(def.Type)
	if !ok {
		return nil, false
	}
	f := &graphql.Field{
		Name:              def.Name,
		Type:              newType,
		Resolve:           resolveWrapper(def.Resolve),
		DeprecationReason: def.DeprecationReason,
		Description:       def.Description,
	}
	if len(def.Args) > 0 {
		f.Args = make(graphql.FieldConfigArgument)
		for _, arg := range def.Args {
			if newType, ok := p.preprocessType(arg.Type); ok {
				f.Args[arg.Name()] = &graphql.ArgumentConfig{
					Type:         newType,
					DefaultValue: arg.DefaultValue,
					Description:  arg.Description(),
				}
			}
		}
	}
	return f, true
}

func (p *preprocessor) preprocessInputObject(obj *graphql.InputObject) *graphql.InputObject {
	return graphql.NewInputObject(graphql.InputObjectConfig{
		Name: obj.Name(),
		Fields: graphql.InputObjectConfigFieldMapThunk(func() graphql.InputObjectConfigFieldMap {
			fields := graphql.InputObjectConfigFieldMap{}
			for name, f := range obj.Fields() {
				newType, ok := p.preprocessType(f.Type)
				if !ok {
					continue
				}
				fields[name] = &graphql.InputObjectFieldConfig{
					Type:         newType,
					DefaultValue: f.DefaultValue,
					Description:  f.Description(),
				}
			}
			return fields
		}),
		Description: obj.Description(),
	})
}

func (p *preprocessor) preprocessUnion(u *graphql.Union) *graphql.Union {
	config := graphql.UnionConfig{
		Description: u.Description(),
		Name:        u.Name(),
		ResolveType: u.ResolveType,
	}
	for _, obj := range u.Types() {
		config.Types = append(config.Types, p.preprocessObject(obj))
	}
	return graphql.NewUnion(config)
}

func (p *preprocessor) preprocessObject(obj *graphql.Object) *graphql.Object {
	return graphql.NewObject(graphql.ObjectConfig{
		Name: obj.Name(),
		Interfaces: graphql.InterfacesThunk(func() []*graphql.Interface {
			ifaces := []*graphql.Interface{}
			for _, iface := range obj.Interfaces() {
				if newType, ok := p.preprocessType(iface); ok {
					ifaces = append(ifaces, newType.(*graphql.Interface))
				}
			}
			return ifaces
		}),
		Fields: graphql.FieldsThunk(func() graphql.Fields {
			fields := graphql.Fields{}
			for name, def := range obj.Fields() {
				f, ok := p.preprocessField(def)
				if !ok {
					continue
				}
				fields[name] = f
			}
			if obj.Error() != nil {
				panic(obj.Error().Error())
			}
			return fields
		}),
		IsTypeOf:    obj.IsTypeOf,
		Description: obj.Description(),
	})
}

func (p *preprocessor) preprocessInterface(iface *graphql.Interface) *graphql.Interface {
	return graphql.NewInterface(graphql.InterfaceConfig{
		Name: iface.Name(),
		Fields: graphql.FieldsThunk(func() graphql.Fields {
			fields := graphql.Fields{}
			for name, def := range iface.Fields() {
				f, ok := p.preprocessField(def)
				if !ok {
					continue
				}
				fields[name] = f
			}
			return fields
		}),
		ResolveType: func(params graphql.ResolveTypeParams) *graphql.Object {
			return p.preprocessObject(iface.ResolveType(params))
		},
		Description: iface.Description(),
	})
}
