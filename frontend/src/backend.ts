// Manages the connection from the browser to the Go server.
import { createPromiseClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { Mastopoof } from "mastopoof-proto/gen/mastopoof/mastopoof_connect";


// XXX move that elsewhere
const transport = createConnectTransport({
    baseUrl: "/_rpc/",
});
export const client = createPromiseClient(Mastopoof, transport);

