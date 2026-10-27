/**
 * Browser-safe entry for the dashboard: `import { MetaRushEdit, planMetadata } from '@metarush/video/lib'`.
 * Nothing reachable from here imports Node-only modules.
 */
export { MetaRushEdit, type MetaRushEditProps } from './MetaRushEdit';
export { planMetadata } from './metadata';
export { TEMPLATE_CATALOG, TEMPLATE_MANIFEST, type TemplateCatalogEntry, type TemplateManifestEntry } from './templates/registry';
export { VERSION as REMOTION_VERSION } from 'remotion';
