import type React from 'react';
import type { CalculateMetadataFunction } from 'remotion';
import { loadMotionBundle } from './load';

/** What `motion check` learns about a component by loading it on a render page. */
export type MotionInspection = {
  exports: string[];
  component: boolean;
  meta: unknown;
  jsonSchema: unknown;
  schemaError?: string;
  preview: { ok: boolean; error?: string };
};

export type MotionInfoProps = { hash: string; url: string; preview: unknown; info?: MotionInspection };

const plain = (v: unknown): unknown => {
  try {
    return JSON.parse(JSON.stringify(v ?? null));
  } catch {
    return null;
  }
};

/**
 * The MetaRushMotionInfo composition's metadata step: loads the bundle in the
 * (network-jailed) render page, converts its zod schema with zod's own
 * converter and checks the sample parameters. The result travels back to
 * Node as data in the resolved props — never as code.
 */
export const calculateMotionInfo: CalculateMetadataFunction<MotionInfoProps> = async ({ props }) => {
  const e = await loadMotionBundle(props.hash, props.url);
  let jsonSchema: unknown = null;
  let schemaError: string | undefined;
  try {
    jsonSchema = plain(e.jsonSchema());
  } catch (err) {
    schemaError = err instanceof Error ? err.message : String(err);
  }
  const p = e.parse(props.preview ?? {});
  const c = e.component as unknown;
  return {
    props: {
      ...props,
      info: {
        exports: e.exports,
        component: typeof c === 'function' || (typeof c === 'object' && c !== null),
        meta: plain(e.meta),
        jsonSchema,
        schemaError,
        preview: p.success ? { ok: true } : { ok: false, error: p.error },
      },
    },
  };
};

export const MotionInfoView: React.FC<MotionInfoProps> = () => null;
