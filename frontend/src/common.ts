import { unsafeCSS } from 'lit'

import { Backend } from "./backend";
import * as pb from "mastopoof-proto/gen/mastopoof/mastopoof_pb";
import * as settingspb from "mastopoof-proto/gen/mastopoof/settings/settings_pb";
import * as mastodon from "./mastodon";
import { createConnectTransport } from "@connectrpc/connect-web";
import * as protobuf from '@bufbuild/protobuf';

import normalizeCSSstr from "./normalize.css?inline";
import baseCSSstr from "./base.css?inline";

import dayjs from 'dayjs';
import relativeTimePlugin from 'dayjs/plugin/relativeTime';
dayjs.extend(relativeTimePlugin);
import utcPlugin from 'dayjs/plugin/utc';
dayjs.extend(utcPlugin);
import timezonePlugin from 'dayjs/plugin/timezone';
dayjs.extend(timezonePlugin);

// TODO: use context https://lit.dev/docs/data/context/
export const displayTimezone = dayjs.tz.guess();
console.log("Display timezone:", displayTimezone);

// Get the settings meta info, which have been generated
// from a textproto.
import settingsInfoJSON from "mastopoof-proto/gen/settings.json?raw";

export const settingsInfo = protobuf.fromJsonString(settingspb.SettingsInfoSchema, settingsInfoJSON);

// Create a global backend access.
// TODO: use context https://lit.dev/docs/data/context/
export let backend = new Backend(createConnectTransport({
  baseUrl: "/_rpc/",
}));

export function setBackend(b: Backend) {
  backend = b;
}

export const sharedCSS = [unsafeCSS(normalizeCSSstr), unsafeCSS(baseCSSstr)];

export function parseStatus(pbStatus: pb.MastodonStatus): mastodon.Status {
  return JSON.parse(pbStatus.content) as mastodon.Status;
}

// This is the data that is provided before typescript main codebase is executed.
// This is also declared in server.go.
export type MastopoofConfig = {
  // A string indicating how the config was obtained. For debugging.
  src: string;
  // Default Mastodon server address to use in login box.
  defServer: string;
  // If true, an invite code is expected.
  invite: boolean;
};

declare global {
  var mastopoofConfig: MastopoofConfig;
}