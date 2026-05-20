package ioc

import (
	"reflect"
	"testing"
)

// 定义一些用于测试的接口和结构体
type TestInterface interface {
	GetName() string
}

type TestServiceA struct {
	Name string
}

func (t *TestServiceA) GetName() string {
	return t.Name
}

type TestServiceB struct {
	ServiceA TestInterface
	Name     string
}

func (t *TestServiceB) GetName() string {
	return t.ServiceA.GetName() + "-" + t.Name
}

type TestServiceC struct {
	ServiceA *TestServiceA
	ServiceB TestInterface
	Name     string
}

func (t *TestServiceC) GetName() string {
	return t.ServiceA.GetName() + "-" + t.ServiceB.GetName() + "-" + t.Name
}

// 测试 NewContainer 函数
func TestNewContainer(t *testing.T) {
	container := NewContainer()

	if container == nil {
		t.Error("Expected container to be initialized, but got nil")
	}

	if container.providers == nil {
		t.Error("Expected providers map to be initialized, but got nil")
	}

	if container.dependencies == nil {
		t.Error("Expected dependencies map to be initialized, but got nil")
	}

	if container.bindingMap == nil {
		t.Error("Expected bindingMap to be initialized, but got nil")
	}

	if container.instanceCache == nil {
		t.Error("Expected instanceCache to be initialized, but got nil")
	}
}

// 测试 Provide 方法 - 基本功能
func TestProvide_Basic(t *testing.T) {
	container := NewContainer()

	constructor := func() *TestServiceA {
		return &TestServiceA{Name: "ServiceA"}
	}

	binder := container.Provide(constructor)

	if binder == nil {
		t.Error("Expected binder to be returned, but got nil")
	}

	serviceAType := reflect.TypeOf((*TestServiceA)(nil))

	// 检查是否已注册
	if _, exists := container.providers[serviceAType]; !exists {
		t.Error("Expected service A to be registered in providers")
	}

	// 使用ToSelf将类型绑定到自身
	binder.ToSelf()

	// 检查绑定映射
	if _, exists := container.bindingMap[serviceAType]; !exists {
		t.Error("Expected service A to be registered in bindingMap")
	}
}

// 测试 Provide 方法 - 验证构造函数类型
func TestProvide_InvalidConstructor(t *testing.T) {
	container := NewContainer()

	// 测试非函数类型的构造函数
	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic for non-function constructor")
		}
	}()

	container.Provide("not a function")
}

// 测试 Provide 方法 - 验证返回值数量
func TestProvide_InvalidReturnCount(t *testing.T) {
	container := NewContainer()

	// 测试没有返回值的构造函数
	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic for constructor with no return values")
		}
	}()

	constructor := func() {}
	container.Provide(constructor)
}

// 测试 Provide 方法 - 验证返回值类型
func TestProvide_InvalidReturnType(t *testing.T) {
	container := NewContainer()

	// 测试返回非指针类型的构造函数
	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic for constructor returning non-pointer type")
		}
	}()

	constructor := func() int { return 42 }
	container.Provide(constructor)
}

// 测试 Provide 方法 - 验证参数类型
func TestProvide_InvalidParamType(t *testing.T) {
	container := NewContainer()

	// 测试参数不是结构体指针的构造函数
	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic for constructor with invalid parameter type")
		}
	}()

	constructor := func(param int) *TestServiceA { return &TestServiceA{} }
	container.Provide(constructor)
}

// 测试 Provide 方法 - 重复注册
func TestProvide_DuplicateRegistration(t *testing.T) {
	container := NewContainer()

	constructor := func() *TestServiceA {
		return &TestServiceA{Name: "ServiceA"}
	}

	// 第一次注册应该成功
	container.Provide(constructor)

	// 第二次注册应该失败
	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic for duplicate registration")
		}
	}()

	container.Provide(constructor)
}

// 测试 ToInterface 方法
func TestProviderBinder_ToInterface(t *testing.T) {
	container := NewContainer()

	constructor := func() *TestServiceA {
		return &TestServiceA{Name: "ServiceA"}
	}

	binder := container.Provide(constructor)

	// 绑定到接口
	var interfacePtr TestInterface
	binder.ToInterface(&interfacePtr)

	interfaceType := reflect.TypeOf((*TestInterface)(nil)).Elem()
	serviceAType := reflect.TypeOf((*TestServiceA)(nil))

	// 检查绑定映射
	bindings, exists := container.bindingMap[interfaceType]
	if !exists || len(bindings) == 0 {
		t.Error("Expected interface to be bound to implementation")
	}

	if bindings[0] != serviceAType {
		t.Error("Expected interface to be bound to correct implementation type")
	}
}

// 测试 ToSelf 方法
func TestProviderBinder_ToSelf(t *testing.T) {
	container := NewContainer()

	constructor := func() *TestServiceA {
		return &TestServiceA{Name: "ServiceA"}
	}

	binder := container.Provide(constructor)

	// 绑定到自身
	binder.ToSelf()

	serviceAType := reflect.TypeOf((*TestServiceA)(nil))

	// 检查绑定映射
	bindings, exists := container.bindingMap[serviceAType]
	if !exists || len(bindings) == 0 {
		t.Error("Expected type to be bound to itself")
	}

	if bindings[0] != serviceAType {
		t.Error("Expected type to be bound to itself")
	}
}

// 测试 Get 方法 - 基本功能
func TestGet_Basic(t *testing.T) {
	container := NewContainer()

	// 注册服务A
	constructorA := func() *TestServiceA {
		return &TestServiceA{Name: "ServiceA"}
	}
	container.Provide(constructorA).ToInterface((*TestInterface)(nil))

	// 调用函数 - 参数必须是结构体指针

	result := Get[TestInterface](container).GetName()

	if result != "ServiceA" {
		t.Errorf("Expected result to be 'ServiceA', but got '%s'", result)
	}
}

// 测试 Get 方法 - 带有结构体参数
func TestGet_StructParam(t *testing.T) {
	container := NewContainer()

	// 注册服务A
	constructorA := func() *TestServiceA {
		return &TestServiceA{Name: "ServiceA"}
	}
	container.Provide(constructorA).ToInterface((*TestInterface)(nil))

	// 调用函数
	result := Get[TestInterface](container).GetName()

	if result != "ServiceA" {
		t.Errorf("Expected result to be 'ServiceA', but got '%s'", result)
	}
}

// 为循环依赖测试定义的类型
type CircularServiceB struct {
	ServiceA *CircularServiceA
}

type CircularServiceA struct {
	ServiceB *CircularServiceB
}

// 测试循环依赖检测
func TestCircularDependencyDetection(t *testing.T) {
	container := NewContainer()

	// 创建循环依赖：ServiceA依赖ServiceB，ServiceB依赖ServiceA
	constructorA := func(dep *CircularServiceB) *CircularServiceA {
		return &CircularServiceA{ServiceB: dep}
	}

	constructorB := func(dep *CircularServiceA) *CircularServiceB {
		return &CircularServiceB{ServiceA: dep}
	}

	container.Provide(constructorA).ToSelf()
	container.Provide(constructorB).ToSelf()

	// 尝试获取ServiceA应该会检测到循环依赖并panic
	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic for circular dependency")
		}
	}()

	Get[*CircularServiceA](container)
}

// 定义接口和实现
type ServiceInterface interface {
	GetName() string
}

type ServiceImplA struct{}

func NewServiceImplA() *ServiceImplA {
	return &ServiceImplA{}
}

func (s *ServiceImplA) GetName() string {
	return "ServiceImplA"
}

type ServiceImplB struct{}

func NewServiceImplB() *ServiceImplB {
	return &ServiceImplB{}
}

func (s *ServiceImplB) GetName() string {
	return "ServiceImplB"
}

// 测试接口切片依赖
func TestInterfaceSliceDependency(t *testing.T) {
	container := NewContainer()

	// 注册多个实现到接口切片
	container.Provide(NewServiceImplA).ToInterface(new(ServiceInterface))
	container.Provide(NewServiceImplB).ToInterface(new(ServiceInterface))

	// 定义一个结构体依赖接口切片
	type Consumer struct {
		Services []ServiceInterface
	}

	// 调用函数，验证接口切片依赖是否正确注入
	result := map[string]bool{}

	services := Get[[]ServiceInterface](container)

	for _, service := range services {
		result[service.GetName()] = true
	}

	expected := map[string]bool{
		"ServiceImplA": true,
		"ServiceImplB": true,
	}

	if !reflect.DeepEqual(result, expected) {
		t.Errorf("Expected result to be %v, but got %v", expected, result)
	}
}

type InfraA interface {
	GetName() string
}

type InfraAImpl struct {
	Name string
}

func NewInfraA() *InfraAImpl {
	return &InfraAImpl{Name: "InfraA"}
}

func (i *InfraAImpl) GetName() string {
	return i.Name
}

type ServiceImplAWithInfra struct {
	Infra InfraA
}

type NewServiceImplAWithInfraDeps struct {
	Infra InfraA
}

func NewServiceImplAWithInfra(deps *NewServiceImplAWithInfraDeps) *ServiceImplAWithInfra {
	return &ServiceImplAWithInfra{Infra: deps.Infra}
}

func (s *ServiceImplAWithInfra) GetName() string {
	return "ServiceImplAWithInfra-" + s.Infra.GetName()
}

type ServiceImplBWithInfra struct {
	Infra InfraA
}

type NewServiceImplBWithInfraDeps struct {
	Infra InfraA
}

func NewServiceImplBWithInfra(deps *NewServiceImplBWithInfraDeps) *ServiceImplBWithInfra {
	return &ServiceImplBWithInfra{Infra: deps.Infra}
}

func (s *ServiceImplBWithInfra) GetName() string {
	return "ServiceImplBWithInfra-" + s.Infra.GetName()
}

// 测试接口切片依赖 - 多个实现共享同一依赖
func TestInterfaceSliceDependencyWithCommonDeps(t *testing.T) {
	container := NewContainer()

	container.Provide(NewInfraA).ToInterface(new(InfraA))

	// 注册多个实现到接口切片
	container.Provide(NewServiceImplAWithInfra).ToInterface(new(ServiceInterface))
	container.Provide(NewServiceImplBWithInfra).ToInterface(new(ServiceInterface))

	// 定义一个结构体依赖接口切片
	type Consumer struct {
		Services []ServiceInterface
	}

	// 调用函数，验证接口切片依赖是否正确注入
	result := map[string]bool{}

	services := Get[[]ServiceInterface](container)
	for _, service := range services {
		result[service.GetName()] = true
	}

	expected := map[string]bool{
		"ServiceImplAWithInfra-InfraA": true,
		"ServiceImplBWithInfra-InfraA": true,
	}

	if !reflect.DeepEqual(result, expected) {
		t.Errorf("Expected result to be %v, but got %v", expected, result)
	}
}

type RouteContrib interface {
	GetRoute() string
}

type RouteContribImplA struct {
	route string
}

func NewRouteContribImplA() *RouteContribImplA {
	return &RouteContribImplA{
		route: "/routeA",
	}
}

func (r *RouteContribImplA) GetRoute() string {
	return r.route
}

type RouteContribImplB struct {
	route string
}

func NewRouteContribImplB() *RouteContribImplB {
	return &RouteContribImplB{
		route: "/routeB",
	}
}

func (r *RouteContribImplB) GetRoute() string {
	return r.route
}

type Router interface {
	GetRoutes() []string
}

type RouterImpl struct {
	routeContribs []RouteContrib
}

type NewRouterImplDeps struct {
	RouteContribs []RouteContrib
}

func NewRouterImpl(deps *NewRouterImplDeps) *RouterImpl {
	return &RouterImpl{
		routeContribs: deps.RouteContribs,
	}
}

func (r *RouterImpl) GetRoutes() []string {
	var routes []string
	for _, contrib := range r.routeContribs {
		routes = append(routes, contrib.GetRoute())
	}
	return routes
}

// 回归测试：重复 Get 不应重复调用构造函数
func TestConstructorCalledOnlyOnceAcrossMultipleGets(t *testing.T) {
	container := NewContainer()

	callCount := 0
	constructorA := func() *TestServiceA {
		callCount++
		return &TestServiceA{Name: "ServiceA"}
	}
	container.Provide(constructorA).ToSelf()

	// 另一个依赖 TestServiceA 的服务
	type DepsWithA struct {
		A *TestServiceA
	}
	serviceBConstructor := func(d *DepsWithA) *TestServiceB {
		return &TestServiceB{Name: "B"}
	}
	container.Provide(serviceBConstructor).ToSelf()

	// 先单独 Get A 触发一次构造
	Get[*TestServiceA](container)
	if callCount != 1 {
		t.Fatalf("after first Get, callCount=%d, want 1", callCount)
	}

	// 再 Get B（B 依赖 A）—— A 已经在全局缓存，不应再调用构造函数
	Get[*TestServiceB](container)
	if callCount != 1 {
		t.Fatalf("after Get B, callCount=%d, want 1 (A should come from cache)", callCount)
	}

	// 再次单独 Get A，依然不应重复构造
	Get[*TestServiceA](container)
	if callCount != 1 {
		t.Fatalf("after second Get A, callCount=%d, want 1", callCount)
	}
}

// 回归测试：slice 依赖中已缓存的实现不应重复构造
func TestSliceDependencyReusesCachedImpls(t *testing.T) {
	container := NewContainer()

	aCount := 0
	constructorA := func() *ServiceImplA {
		aCount++
		return &ServiceImplA{}
	}
	bCount := 0
	constructorB := func() *ServiceImplB {
		bCount++
		return &ServiceImplB{}
	}
	binderA := container.Provide(constructorA)
	binderA.ToSelf()
	binderA.ToInterface(new(ServiceInterface))
	binderB := container.Provide(constructorB)
	binderB.ToSelf()
	binderB.ToInterface(new(ServiceInterface))

	// 先单独 Get 一个实现
	Get[*ServiceImplA](container)
	if aCount != 1 {
		t.Fatalf("aCount=%d", aCount)
	}

	// 再 Get 整个 slice —— 已缓存的实现不应重复构造
	services := Get[[]ServiceInterface](container)
	if len(services) != 2 {
		t.Fatalf("len=%d", len(services))
	}
	if aCount != 1 {
		t.Errorf("aCount=%d after slice Get, want 1", aCount)
	}
	if bCount != 1 {
		t.Errorf("bCount=%d after slice Get, want 1", bCount)
	}
}

// 测试接口切片依赖 - 实现依赖接口切片的结构体
func TestInterfaceSliceDependencyWithStruct(t *testing.T) {
	container := NewContainer()

	// 注册多个实现到接口切片
	container.Provide(NewRouteContribImplA).ToInterface(new(RouteContrib))
	container.Provide(NewRouteContribImplB).ToInterface(new(RouteContrib))

	// 注册依赖接口切片的结构体
	container.Provide(NewRouterImpl).ToInterface(new(Router))

	// 调用函数，验证接口切片依赖是否正确注入

	routes := Get[Router](container).GetRoutes()
	expected := []string{"/routeA", "/routeB"}
	for i, route := range routes {
		if route != expected[i] {
			t.Errorf("Expected route to be '%s', but got '%s'", expected[i], route)
		}
	}

}

// TestProvide_InterfaceParam verifies that a constructor can accept an interface
// type directly as its parameter (without wrapping it in a *Deps struct).
func TestProvide_InterfaceParam(t *testing.T) {
	c := NewContainer()

	// Register a constructor that returns a concrete impl bound to an interface.
	c.Provide(func() *TestServiceA { return &TestServiceA{Name: "foo"} }).ToInterface(new(TestInterface))

	// Another constructor that takes the interface directly as its parameter.
	c.Provide(func(iface TestInterface) *TestServiceB {
		return &TestServiceB{ServiceA: iface, Name: "bar"}
	}).ToSelf()

	svc := Get[*TestServiceB](c)

	if svc.GetName() != "foo-bar" {
		t.Errorf("Expected 'foo-bar', got '%s'", svc.GetName())
	}
}
