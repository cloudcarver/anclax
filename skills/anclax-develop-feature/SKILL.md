---
name: anclax-develop-feature
description: Anclax is a Golang backend framework. This skill should be used when writing, reviewing or refactoring backend backed by Anclax. Triggers on tasks involving HTTP APIs, websocket, database interactions, business logic, async tasks. 
---

# Anclax Best Practices

The idea of Anclax is to use compiler to check if the business logic meets the spec definition. 

## Anclax Setup

Anclax framework generates code via anclax CLI, the config is in `anclax.yaml`. Check the config to see where the generated code is located.

## References and Examples
 - [CURD operations](./crud.md): Work on CRUD operations in Anclax framework. About OpenAPI spec, handler, service, model
 - [OpenAPI Spec](./openapi-spec.md): Best practices when writing OpenAPI spec
 - [Business Logic](./business-logic.md): Best practices when writing business logic
