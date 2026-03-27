"""Tests for contract detection."""

from workers.contracts.detector import (
    DetectedContract,
    detect_contracts,
    detect_graphql_schema,
    detect_openapi,
    detect_protobuf,
)


class TestDetectOpenAPI:
    def test_json_openapi3(self):
        content = """{
  "openapi": "3.0.1",
  "info": {"title": "Pet Store", "version": "1.0"},
  "paths": {
    "/pets": {
      "get": {"summary": "List all pets"},
      "post": {"summary": "Create a pet"}
    },
    "/pets/{petId}": {
      "get": {"summary": "Get a pet by ID"}
    }
  }
}"""
        result = detect_openapi("api/openapi.json", content)
        assert result is not None
        assert result.contract_type == "openapi"
        assert result.version == "3.0.1"
        assert len(result.endpoints) == 3
        paths = {(ep.path, ep.method) for ep in result.endpoints}
        assert ("/pets", "GET") in paths
        assert ("/pets", "POST") in paths
        assert ("/pets/{petId}", "GET") in paths

    def test_yaml_swagger2(self):
        content = """swagger: "2.0"
info:
  title: Users API
paths:
  /users:
    get:
      summary: List users
    post:
      summary: Create user
"""
        result = detect_openapi("swagger.yaml", content)
        assert result is not None
        assert result.contract_type == "openapi"
        assert result.version == "2.0"
        assert len(result.endpoints) == 2

    def test_non_openapi_json(self):
        result = detect_openapi("config.json", '{"key": "value"}')
        assert result is None

    def test_non_openapi_yaml(self):
        result = detect_openapi("deploy.yaml", "apiVersion: apps/v1\nkind: Deployment")
        assert result is None


class TestDetectProtobuf:
    def test_with_service(self):
        content = """syntax = "proto3";

package myapp.v1;

service UserService {
  rpc GetUser(GetUserRequest) returns (GetUserResponse);
  rpc ListUsers(ListUsersRequest) returns (ListUsersResponse);
}

message GetUserRequest { string id = 1; }
message GetUserResponse { string name = 1; }
"""
        result = detect_protobuf("proto/user.proto", content)
        assert result is not None
        assert result.contract_type == "protobuf"
        assert result.version == "proto3"
        assert len(result.endpoints) == 2
        assert result.endpoints[0].path == "UserService.GetUser"
        assert result.endpoints[0].method == "RPC"

    def test_without_service(self):
        content = """syntax = "proto3";
message Foo { string bar = 1; }
"""
        result = detect_protobuf("types.proto", content)
        assert result is None

    def test_non_proto_file(self):
        result = detect_protobuf("main.go", "service UserService {}")
        assert result is None


class TestDetectGraphQL:
    def test_schema_with_query_and_mutation(self):
        content = """type Query {
  users: [User!]!
  user(id: ID!): User
}

type Mutation {
  createUser(name: String!): User!
  deleteUser(id: ID!): Boolean!
}

type User {
  id: ID!
  name: String!
}
"""
        result = detect_graphql_schema("schema.graphqls", content)
        assert result is not None
        assert result.contract_type == "graphql"
        assert len(result.endpoints) == 4
        paths = {(ep.path, ep.method) for ep in result.endpoints}
        assert ("users", "QUERY") in paths
        assert ("user", "QUERY") in paths
        assert ("createUser", "MUTATION") in paths

    def test_non_schema_graphql(self):
        content = """query GetUser($id: ID!) {
  user(id: $id) { name }
}
"""
        result = detect_graphql_schema("queries.graphql", content)
        assert result is None  # No type Query/Mutation definitions


class TestDetectContracts:
    def test_mixed_files(self):
        files = [
            ("openapi.json", '{"openapi": "3.0.0", "paths": {"/a": {"get": {}}}}'),
            ("service.proto", 'syntax = "proto3";\nservice Svc {\n  rpc Do(Req) returns (Resp);\n}'),
            ("main.go", "package main\nfunc main() {}"),
            ("schema.graphql", "type Query {\n  hello: String\n}"),
        ]
        results = detect_contracts(files)
        assert len(results) == 3
        types = {r.contract_type for r in results}
        assert types == {"openapi", "protobuf", "graphql"}

    def test_empty_files(self):
        assert detect_contracts([]) == []

    def test_no_contracts(self):
        files = [
            ("main.py", "print('hello')"),
            ("config.yaml", "key: value"),
        ]
        assert detect_contracts(files) == []
