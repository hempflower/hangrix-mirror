package ioc

import (
	"maps"
	"reflect"
	"sync"
)

// Container is the IoC container
type Container struct {
	providers     map[reflect.Type]*serviceProvider // Multiple providers for each type
	dependencies  map[reflect.Type][]reflect.Type   // Type dependencies
	bindingMap    map[reflect.Type][]reflect.Type   // Binding map for interfaces to concrete types
	instanceCache map[reflect.Type]reflect.Value    // Cache for created instances
	mu            sync.RWMutex
}

// serviceProvider holds constructor and singleton flag
type serviceProvider struct {
	constructor reflect.Value
}

// isValidReturnType reports whether a constructor return type is supported.
// Two forms are allowed:
//   - *Struct: the dominant case — a concrete pointer-to-struct that the
//     module owns.
//   - Interface: useful when the underlying value comes from a third-party
//     library that returns an interface (e.g. redis.NewUniversalClient
//     returns redis.UniversalClient, whose concrete type depends on config).
func isValidReturnType(t reflect.Type) bool {
	if t.Kind() == reflect.Ptr && t.Elem().Kind() == reflect.Struct {
		return true
	}
	if t.Kind() == reflect.Interface {
		return true
	}
	return false
}

// NewContainer creates a new IoC container
func NewContainer() *Container {
	return &Container{
		providers:     make(map[reflect.Type]*serviceProvider),
		dependencies:  make(map[reflect.Type][]reflect.Type),
		bindingMap:    make(map[reflect.Type][]reflect.Type),
		instanceCache: make(map[reflect.Type]reflect.Value),
	}
}

type ProviderBinder struct {
	container       *Container
	provideType     reflect.Type
	dependencyTypes []reflect.Type
}

func (pc *ProviderBinder) ToInterface(ifacePtr any) {
	ifaceType := reflect.TypeOf(ifacePtr)
	if ifaceType.Kind() != reflect.Pointer || ifaceType.Elem().Kind() != reflect.Interface {
		panic("ToInterface argument must be a pointer to an interface")
	}
	pc.container.mu.Lock()
	defer pc.container.mu.Unlock()
	pc.container.bindingMap[ifaceType.Elem()] = append(pc.container.bindingMap[ifaceType.Elem()], pc.provideType)
}

func (pc *ProviderBinder) ToSelf() {
	pc.container.mu.Lock()
	defer pc.container.mu.Unlock()
	pc.container.bindingMap[pc.provideType] = append(pc.container.bindingMap[pc.provideType], pc.provideType)
}

// Provide registers a constructor function in the container
// Constructor must be a function that returns a value
func (c *Container) Provide(constructor any) *ProviderBinder {
	c.mu.Lock()
	defer c.mu.Unlock()

	constructorValue := reflect.ValueOf(constructor)
	constructorType := constructorValue.Type()

	if constructorType.Kind() != reflect.Func {
		panic("constructor must be a function")
	}

	// 返回值必须是一个 struct 指针，或者一个接口（用于外部库构造，例如返回 redis.UniversalClient）
	if constructorType.NumOut() != 1 {
		panic("constructor must return exactly one value")
	}
	returnType := constructorType.Out(0)
	if !isValidReturnType(returnType) {
		panic("constructor return type must be a pointer to a struct or an interface, got: " + returnType.String())
	}

	// 检查是不是已经注册过了，每个类型只能有一个提供者
	if _, exists := c.providers[returnType]; exists {
		panic("constructor for this type already registered")
	}

	dependencies := []reflect.Type{}

	// 参数只有0或1个，如果有参数，必须是一个结构体指针
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
			// 解析结构体字段，记录依赖关系
			for i := 0; i < paramType.Elem().NumField(); i++ {
				field := paramType.Elem().Field(i)
				// 接口
				if field.Type.Kind() == reflect.Interface {
					dependencies = append(dependencies, field.Type)
					// 结构体指针
				} else if field.Type.Kind() == reflect.Ptr && field.Type.Elem().Kind() == reflect.Struct {
					dependencies = append(dependencies, field.Type)
					// 接口切片 / 结构体指针切片
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
	c.providers[returnType] = &serviceProvider{
		constructor: constructorValue,
	}
	c.dependencies[returnType] = dependencies

	return &ProviderBinder{
		container:       c,
		provideType:     returnType,
		dependencyTypes: dependencies,
	}
}

func (c *Container) constructType(target reflect.Type) reflect.Value {
	root := target

	if instance, ok := c.instanceCache[root]; ok {
		return instance
	}

	localInstanceCache := map[reflect.Type]reflect.Value{}

	stack := []reflect.Type{root}
	instanceState := map[reflect.Type]uint8{}

	for len(stack) > 0 {
		t := stack[len(stack)-1]

		if _, ok := localInstanceCache[t]; ok {
			stack = stack[:len(stack)-1]
			continue
		}
		if instanceState[t] == 0 {
			instanceState[t] = 1

			_, ok := c.providers[t]
			if !ok {
				panic("no provider found for type: " + t.String())
			}

			for _, dep := range c.dependencies[t] {
				// 处理 slice 依赖
				if dep.Kind() == reflect.Slice {
					elemType := dep.Elem()

					// 空切片依赖是合法的 —— 没有任何模块注册该接口的实现时，
					// 消费方拿到一个长度为 0 的切片即可。强制 panic 会让
					// "可选扩展点" 在没人接线时无法启动。
					bound := c.bindingMap[elemType]
					for _, impl := range bound {
						if instanceState[impl] == 1 {
							panic("circular dependency detected: " + t.String() + " -> " + impl.String())
						}
						if _, cached := localInstanceCache[impl]; cached {
							continue
						}
						if instance, globalCached := c.instanceCache[impl]; globalCached {
							localInstanceCache[impl] = instance
							instanceState[impl] = 2
							continue
						}
						stack = append(stack, impl)
					}
					continue
				}

				bound, ok := c.bindingMap[dep]
				if !ok || len(bound) == 0 {
					panic("no provider found for type: " + dep.String())
				}

				depType := bound[0]

				switch instanceState[depType] {
				case 1:
					panic("circular dependency detected: " + t.String() + " -> " + depType.String())
				case 0:
					if _, cached := localInstanceCache[depType]; cached {
						continue
					}
					if instance, globalCached := c.instanceCache[depType]; globalCached {
						localInstanceCache[depType] = instance
						instanceState[depType] = 2
						continue
					}
					stack = append(stack, depType)
				}

			}
			continue
		}

		provider := c.providers[t]

		args := []reflect.Value{}

		// 获取构造函数参数类型
		constructorType := provider.constructor.Type()
		if constructorType.NumIn() == 1 {
			paramType := constructorType.In(0)
			if paramType.Kind() == reflect.Interface {
				// Direct interface parameter — resolve from local cache.
				depImplType := c.bindingMap[paramType][0]
				instance := localInstanceCache[depImplType]
				args = append(args, instance)
			} else {
				// Pointer-to-struct parameter (Deps pattern) — create the struct
				// and populate its fields from the cache.
				paramValue := reflect.New(paramType.Elem())
				for i := 0; i < paramType.Elem().NumField(); i++ {
					field := paramType.Elem().Field(i)
					depType := field.Type

					// 处理接口切片依赖
					if depType.Kind() == reflect.Slice && depType.Elem().Kind() == reflect.Interface {
						bound := c.bindingMap[depType.Elem()]
						slice := reflect.MakeSlice(depType, 0, len(bound))
						for _, impl := range bound {
							instance := localInstanceCache[impl]
							slice = reflect.Append(slice, instance)
						}
						paramValue.Elem().Field(i).Set(slice)
					} else {
						depImplType := c.bindingMap[depType][0]
						instance := localInstanceCache[depImplType]
						paramValue.Elem().Field(i).Set(instance)
					}
				}
				args = append(args, paramValue)
			}
		}

		instance := provider.constructor.Call(args)[0]
		localInstanceCache[t] = instance
		instanceState[t] = 2

		stack = stack[:len(stack)-1]
	}

	// 将本次构建的实例加入全局缓存
	maps.Copy(c.instanceCache, localInstanceCache)

	return localInstanceCache[root]
}

func (c *Container) resolve(targetType reflect.Type) reflect.Value {
	// 检查绑定
	boundList, exists := c.bindingMap[targetType]
	if !exists || len(boundList) == 0 {
		panic("no provider found for type: " + targetType.String())
	}

	// 只取第一个绑定
	root := boundList[0]
	return c.constructType(root)
}

func (c *Container) resolveAll(targetType reflect.Type) []reflect.Value {
	boundList, exists := c.bindingMap[targetType]
	if !exists || len(boundList) == 0 {
		panic("no provider found for type: " + targetType.String())
	}

	var instances []reflect.Value

	for _, bound := range boundList {
		instance := c.constructType(bound)
		instances = append(instances, instance)
	}
	return instances
}

func Get[T any](c *Container) T {
	targetType := reflect.TypeOf((*T)(nil)).Elem()
	if targetType.Kind() == reflect.Pointer {
		return c.resolve(targetType).Interface().(T)
	} else if targetType.Kind() == reflect.Slice && targetType.Elem().Kind() == reflect.Interface {
		targetType = targetType.Elem()
		instances := c.resolveAll(targetType)
		sliceValue := reflect.MakeSlice(reflect.SliceOf(targetType), len(instances), len(instances))
		for i, instance := range instances {
			sliceValue.Index(i).Set(instance)
		}
		return sliceValue.Interface().(T)
	} else if targetType.Kind() == reflect.Interface {
		return c.resolve(targetType).Interface().(T)
	} else {
		panic("Get only supports pointer, interface, or slice of interface types")
	}
}
