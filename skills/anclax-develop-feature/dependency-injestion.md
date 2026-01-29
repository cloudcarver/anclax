# Dependency Injection

Anclax uses wire for dependency injection.

We prefer the singleton pattern.

Use provider to create the object:
```go
type MyModule struct { token string }
func NewMyModule(config *config.Config) MyModuleInterface {
    return &MyModule{token: config.MyModuleToken}
}
```

Register provider in `./wire/wire.go`, ignore `./wire/wire_gen.go`. 

You can pass an object registered in the wire in another provider:
```go
func NewMyService(myModule MyModuleInterface, querier model.ModelInterface) MyServiceInterface {
    return &MyService{myModule: myModule, model: querier}
}
```
Then wire_gen.go calls the factory methods in the right order.

If a provider is updated, run `anclax gen` to regenerate code.
