import { readFileSync } from "node:fs";
import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";
import { parse } from "yaml";

export const contractPath = "/workspaces/{workspaceId}/content-dna";
const contract = parse(readFileSync("api/openapi.yaml", "utf8"));
const operations = contract.paths[contractPath];
const validator = new Ajv2020({ strictSchema: false, addUsedSchema: false });
addFormats(validator);

function compile<T = unknown>(schema: object) {
  return validator.compile<T>({ ...schema, components: contract.components });
}

export const validateWorkspaceId = compile(contract.components.parameters.WorkspaceId.schema);
export const validateSave = compile<{ baseVersionId: string | null; payload: unknown }>(
  operations.put.requestBody.content["application/json"].schema,
);
const responseValidators = new Map();
export function verifyResponse(method: "get" | "put", status: number, body: unknown) {
  const key = `${method}:${status}`;
  if (!responseValidators.has(key)) {
    let response = operations[method].responses[status];
    if (!response) throw new Error(`Undocumented HTTP response ${key}`);
    if (response.$ref) response = contract.components.responses[response.$ref.split("/").at(-1)];
    responseValidators.set(key, compile(response.content["application/json"].schema));
  }
  const validate = responseValidators.get(key);
  if (!validate(body))
    throw new Error(`Invalid HTTP response ${key}: ${validator.errorsText(validate.errors)}`);
}
