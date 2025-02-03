import { expect, test } from 'vitest';
import { parseStatus } from './common';
import * as protobuf from '@bufbuild/protobuf';

import * as pb from "mastopoof-proto/gen/mastopoof/mastopoof_pb";

test('parses empty status for checking test setup', () => {
  const s = parseStatus(protobuf.create(pb.MastodonStatusSchema, { content: '{"id": "42"}' }));
  expect(s.id).eq("42");
});