package ioc

import "reflect"

// Module is a pre-aggregated collection of provider registrations.
// Providers are registered on the module first; the module is then loaded
// into a Container via Container.Load, which merges all registrations at once.
type Module struct {
	providers    map[reflect.Type]*serviceProvider
	dependencies map[reflect.Type][]reflect.Type
	bindingMap   map[reflect.Type][]reflect.Type
}

// NewModule creates a new, empty Module.
func NewModule() *Module {
	return &Module{
		providers:    make(map[reflect.Type]*serviceProvider),
		dependencies: make(map[reflect.Type][]reflect.Type),
		bindingMap:   make(map[reflect.Type][]reflect.Type),
	}
}

// ModuleProviderBinder is returned by Module.Provide and is used to configure
// the interface bindings for a registered constructor.
type ModuleProviderBinder struct {
	module      *Module
	provideType reflect.Type
}

// ToInterface binds the provided type to the given interface.
// ifacePtr must be a pointer to an interface (e.g. new(MyInterface) or (*MyInterface)(nil)).
func (b *ModuleProviderBinder) ToInterface(ifacePtr any) {
	ifaceType := reflect.TypeOf(ifacePtr)
	if ifaceType.Kind() != reflect.Pointer || ifaceType.Elem().Kind() != reflect.Interface {
		panic("ToInterface argument must be a pointer to an interface")
	}
	b.module.bindingMap[ifaceType.Elem()] = append(b.module.bindingMap[ifaceType.Elem()], b.provideType)
}

// ToSelf binds the provided type to itself (use when resolving by concrete pointer type).
func (b *ModuleProviderBinder) ToSelf() {
	b.module.bindingMap[b.provideType] = append(b.module.bindingMap[b.provideType], b.provideType)
}

// Provide registers a constructor function in the module.
// The same rules apply as for Container.Provide:
//   - constructor must be a function
//   - it must return exactly one value that is a pointer to a struct
//   - it may take at most one parameter, which must be a pointer to a struct whose
//     fields are interfaces, pointers-to-structs, or slices of interfaces
func (m *Module) Provide(constructor any) *ModuleProviderBinder {
	constructorValue := reflect.ValueOf(constructor)
	constructorType := constructorValue.Type()

	if constructorType.Kind() != reflect.Func {
		panic("constructor must be a function")
	}

	if constructorType.NumOut() != 1 {
		panic("constructor must return exactly one value")
	}
	returnType := constructorType.Out(0)
	if !isValidReturnType(returnType) {
		panic("constructor return type must be a pointer to a struct or an interface, got: " + returnType.String())
	}

	if _, exists := m.providers[returnType]; exists {
		panic("constructor for this type already registered")
	}

	dependencies := []reflect.Type{}

	if constructorType.NumIn() > 1 {
		panic("constructor must have at most one parameter")
	}
	if constructorType.NumIn() == 1 {
		paramType := constructorType.In(0)
		if paramType.Kind() == reflect.Interface {
			// Accept an interface parameter — the interface itself is the
			// dependency. Useful when a constructor takes a third-party
			// interface type directly (e.g. redis.UniversalClient).
			dependencies = append(dependencies, paramType)
		} else if paramType.Kind() == reflect.Pointer && paramType.Elem().Kind() == reflect.Struct {
			for i := 0; i < paramType.Elem().NumField(); i++ {
				field := paramType.Elem().Field(i)
				if field.Type.Kind() == reflect.Interface {
					dependencies = append(dependencies, field.Type)
				} else if field.Type.Kind() == reflect.Ptr && field.Type.Elem().Kind() == reflect.Struct {
					dependencies = append(dependencies, field.Type)
				} else if field.Type.Kind() == reflect.Slice && field.Type.Elem().Kind() == reflect.Interface {
					dependencies = append(dependencies, field.Type)
				} else if field.Type.Kind() == reflect.Slice && field.Type.Elem().Kind() == reflect.Ptr && field.Type.Elem().Elem().Kind() == reflect.Struct {
					dependencies = append(dependencies, field.Type)
				} else {
					panic("constructor parameter fields must be pointers to structs or interfaces")
				}
			}
		} else {
			panic("constructor parameter must be a pointer to a struct or an interface, current type: " + paramType.String())
		}
	}

	m.providers[returnType] = &serviceProvider{constructor: constructorValue}
	m.dependencies[returnType] = dependencies

	return &ModuleProviderBinder{
		module:      m,
		provideType: returnType,
	}
}

// Load merges one or more modules into the container.
// All provider registrations and interface bindings from each module are copied
// into the container. Panics if a type that is already registered in the container
// is also registered in a loaded module.
func (c *Container) Load(modules ...*Module) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, m := range modules {
		for k, v := range m.providers {
			if _, exists := c.providers[k]; exists {
				panic("constructor for this type already registered: " + k.String())
			}
			c.providers[k] = v
		}
		for k, v := range m.dependencies {
			c.dependencies[k] = v
		}
		for k, vals := range m.bindingMap {
			c.bindingMap[k] = append(c.bindingMap[k], vals...)
		}
	}
}
