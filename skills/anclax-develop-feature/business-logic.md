# Business Logic

The backend framework simply glues external services together, typically it could be just a database.

All these external services are called modules. 

Follow these rules when writing business logic:
1. When integrating modules, wrap them in a module type with a minimal interface. 
2. Modules should be mocked. Add the mock generation config in `anclax.yaml` and then run `anclax gen` to generate the mock code. When interfaces are modified, run `anclax gen` to update the mock code.
3. The business logic should be in service layer. The service layer translates all different types to just the types generated from OpenAPI spec. It accepts apigen types and return apigen types.
4. Create unit tests for business logic, use mocks for modules. 
5. Errors has two types: expected errors and unexpected errors. Expected errors are parts of business logic like user not found, insufficient balance. These errors will be handled properly in handler layer. 
6. Use wire to inject dependencies. 