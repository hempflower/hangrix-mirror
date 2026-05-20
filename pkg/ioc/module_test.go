package ioc

import (
	"reflect"
	"testing"
)

// ---- types reused from container_test.go are in the same package ----
// (ServiceImplA, ServiceImplB, ServiceInterface, InfraA, InfraAImpl, etc.)

// TestNewModule verifies that NewModule initialises all internal maps.
func TestNewModule(t *testing.T) {
	m := NewModule()
	if m == nil {
		t.Fatal("Expected module to be non-nil")
	}
	if m.providers == nil {
		t.Error("Expected providers map to be initialised")
	}
	if m.dependencies == nil {
		t.Error("Expected dependencies map to be initialised")
	}
	if m.bindingMap == nil {
		t.Error("Expected bindingMap to be initialised")
	}
}

// TestModule_Provide_Basic verifies that a constructor is registered in the module.
func TestModule_Provide_Basic(t *testing.T) {
	m := NewModule()
	binder := m.Provide(NewServiceImplA)

	if binder == nil {
		t.Fatal("Expected binder to be non-nil")
	}

	serviceAType := reflect.TypeOf((*ServiceImplA)(nil))
	if _, ok := m.providers[serviceAType]; !ok {
		t.Error("Expected ServiceImplA to be registered in module providers")
	}
}

// TestModule_Provide_ToSelf verifies ToSelf binding on a module.
func TestModule_Provide_ToSelf(t *testing.T) {
	m := NewModule()
	m.Provide(NewServiceImplA).ToSelf()

	serviceAType := reflect.TypeOf((*ServiceImplA)(nil))
	bindings, ok := m.bindingMap[serviceAType]
	if !ok || len(bindings) == 0 {
		t.Fatal("Expected ServiceImplA to be bound to itself in bindingMap")
	}
	if bindings[0] != serviceAType {
		t.Error("Expected binding to point to ServiceImplA itself")
	}
}

// TestModule_Provide_ToInterface verifies ToInterface binding on a module.
func TestModule_Provide_ToInterface(t *testing.T) {
	m := NewModule()
	m.Provide(NewServiceImplA).ToInterface(new(ServiceInterface))

	ifaceType := reflect.TypeOf((*ServiceInterface)(nil)).Elem()
	serviceAType := reflect.TypeOf((*ServiceImplA)(nil))

	bindings, ok := m.bindingMap[ifaceType]
	if !ok || len(bindings) == 0 {
		t.Fatal("Expected ServiceInterface to be bound in module bindingMap")
	}
	if bindings[0] != serviceAType {
		t.Error("Expected ServiceInterface binding to point to ServiceImplA")
	}
}

// TestModule_Provide_InvalidConstructor panics for non-function constructors.
func TestModule_Provide_InvalidConstructor(t *testing.T) {
	m := NewModule()
	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic for non-function constructor")
		}
	}()
	m.Provide("not a function")
}

// TestModule_Provide_DuplicateRegistration panics when the same type is registered twice.
func TestModule_Provide_DuplicateRegistration(t *testing.T) {
	m := NewModule()
	m.Provide(NewServiceImplA)
	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic for duplicate module registration")
		}
	}()
	m.Provide(NewServiceImplA)
}

// TestContainer_Load_SingleModule verifies that providers from a module are
// merged into the container and can be resolved.
func TestContainer_Load_SingleModule(t *testing.T) {
	m := NewModule()
	m.Provide(NewServiceImplA).ToInterface(new(ServiceInterface))

	c := NewContainer()
	c.Load(m)

	result := Get[ServiceInterface](c)
	if result.GetName() != "ServiceImplA" {
		t.Errorf("Expected 'ServiceImplA', got '%s'", result.GetName())
	}
}

// TestContainer_Load_MultipleModules verifies that providers from several modules
// are all merged correctly.
func TestContainer_Load_MultipleModules(t *testing.T) {
	mA := NewModule()
	mA.Provide(NewServiceImplA).ToInterface(new(ServiceInterface))

	mB := NewModule()
	mB.Provide(NewServiceImplB).ToInterface(new(ServiceInterface))

	c := NewContainer()
	c.Load(mA, mB)

	services := Get[[]ServiceInterface](c)
	names := map[string]bool{}
	for _, s := range services {
		names[s.GetName()] = true
	}

	if !names["ServiceImplA"] || !names["ServiceImplB"] {
		t.Errorf("Expected both ServiceImplA and ServiceImplB, got %v", names)
	}
}

// TestContainer_Load_ConflictPanics verifies that loading a module whose type is
// already registered in the container causes a panic.
func TestContainer_Load_ConflictPanics(t *testing.T) {
	c := NewContainer()
	c.Provide(NewServiceImplA).ToSelf()

	m := NewModule()
	m.Provide(NewServiceImplA).ToSelf()

	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic when loading a module with a conflicting type")
		}
	}()
	c.Load(m)
}

// TestContainer_Load_WithDependencies verifies that a loaded module with
// cross-module dependencies resolves correctly.
func TestContainer_Load_WithDependencies(t *testing.T) {
	infraModule := NewModule()
	infraModule.Provide(NewInfraA).ToInterface(new(InfraA))

	serviceModule := NewModule()
	serviceModule.Provide(NewServiceImplAWithInfra).ToInterface(new(ServiceInterface))

	c := NewContainer()
	c.Load(infraModule, serviceModule)

	result := Get[ServiceInterface](c)
	if result.GetName() != "ServiceImplAWithInfra-InfraA" {
		t.Errorf("Expected 'ServiceImplAWithInfra-InfraA', got '%s'", result.GetName())
	}
}

// TestModule_Provide_InterfaceParam verifies that a constructor accepting an
// interface directly (without a *Deps struct) can be registered in a Module.
func TestModule_Provide_InterfaceParam(t *testing.T) {
	m := NewModule()
	m.Provide(func() *TestServiceA { return &TestServiceA{Name: "mod-a"} }).ToInterface(new(TestInterface))
	m.Provide(func(iface TestInterface) *TestServiceB {
		return &TestServiceB{ServiceA: iface, Name: "mod-b"}
	}).ToSelf()

	c := NewContainer()
	c.Load(m)

	svc := Get[*TestServiceB](c)
	if svc.GetName() != "mod-a-mod-b" {
		t.Errorf("Expected 'mod-a-mod-b', got '%s'", svc.GetName())
	}
}

// TestContainer_Load_MixedWithDirectProvide verifies that Load can be combined
// with direct Container.Provide calls.
func TestContainer_Load_MixedWithDirectProvide(t *testing.T) {
	m := NewModule()
	m.Provide(NewInfraA).ToInterface(new(InfraA))

	c := NewContainer()
	c.Load(m)
	c.Provide(NewServiceImplAWithInfra).ToInterface(new(ServiceInterface))

	result := Get[ServiceInterface](c)
	if result.GetName() != "ServiceImplAWithInfra-InfraA" {
		t.Errorf("Expected 'ServiceImplAWithInfra-InfraA', got '%s'", result.GetName())
	}
}
