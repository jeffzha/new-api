import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
const catalog = JSON.parse(await readFile(new URL('./wangpaiai-catalog-20261010.json', import.meta.url), 'utf8'));
const source = await readFile(new URL('./wangpaiai-plugin.js', import.meta.url), 'utf8');
const plugin = await import('data:text/javascript;base64,' + Buffer.from(source).toString('base64'));
const mapping = { 'h3-1': 'MiniMax-H3', 'h3-vip': 'MiniMax-H3-Sensitive', 'wp_a4f5de6f0c3a': 'MiniMax-H3-2K' };
const ctx = (model, requestBody) => ({ model, upstreamModel: model, baseUrl: 'https://upstream.example', apiKey: 'synthetic-test-key', requestBody });
const images = ['https://example.com/image.png'];
let count = 0;
for (const model of catalog.models) {
  for (const tier of model.priceTiers) {
    const context = ctx(mapping[model.id], { prompt: 'Synthetic verification', images, seconds: Number(tier.inputs.duration), resolution: tier.inputs.quality || '2K' });
    const descriptor = plugin.buildSubmitRequest(context);
    assert.equal(descriptor.body.modelId, model.id);
    for (const [key, value] of Object.entries(tier.inputs)) assert.equal(descriptor.body.inputs[key], value);
    assert.equal(descriptor.body.inputs.image_url, images[0]);
    assert.equal(plugin.extractUsage(context).vendor_credits, tier.priceCredits);
    count++;
  }
}
assert.equal(count, 97);
const fixed = ctx('MiniMax-H3-2K', { prompt: 'Synthetic verification', images, videos: ['https://example.com/video.mp4'], audios: ['https://example.com/audio.mp3'], aspect_ratio: '3:4' });
const inputs = plugin.buildSubmitRequest(fixed).body.inputs;
assert.equal(inputs.aspect_ratio, '3:4');
assert.equal(inputs.duration, '15');
assert.equal(inputs.video_url, fixed.requestBody.videos[0]);
assert.equal(inputs.audio_url, fixed.requestBody.audios[0]);
assert.deepEqual(plugin.extractUsage(fixed), { output_credits: 19, material_credits: 0, input_images: 1, vendor_credits: 19, mode: 'text', seconds: 15, resolution: '2K' });
for (const seconds of [0, 3, 16, -1, 1e12, 4.5]) assert.throws(() => plugin.buildSubmitRequest(ctx('MiniMax-H3', { prompt: 'Synthetic verification', images, seconds })));
for (const resolution of ['768P', '4K']) assert.throws(() => plugin.buildSubmitRequest(ctx('MiniMax-H3', { prompt: 'Synthetic verification', images, resolution })));
assert.throws(() => plugin.buildSubmitRequest(ctx('unknown', { prompt: 'Synthetic verification', images })));
assert.throws(() => plugin.buildSubmitRequest(ctx('MiniMax-H3', { prompt: 'Synthetic verification', images: Array.from({length:10}, (_,i) => 'https://example.com/' + i + '.png') })));
assert.throws(() => plugin.buildSubmitRequest(ctx('MiniMax-H3', { prompt: 'Synthetic verification' })));
for (const cost of [0, 27.3]) assert.deepEqual(plugin.extractUsageOnComplete({}, { status: 'SUCCESS' }, { generation: { cost } }), { vendor_credits: cost, output_credits: cost, material_credits: 0 });
const submitted = plugin.parseSubmitResponse(fixed, { body: { generation: { id: 'synthetic-id', cost: 29 } } });
assert.deepEqual(plugin.extractUsageOnComplete({ state: submitted.state }, { status: 'SUCCESS' }, { generation: { cost: 29 } }), { vendor_credits: 29, output_credits: 19, material_credits: 10 });
for (const cost of [undefined, null, '19', -1, Infinity, 2147483648]) assert.equal(plugin.extractUsageOnComplete({}, { status: 'SUCCESS' }, { generation: { cost } }), null);
assert.equal(plugin.extractUsageOnComplete({}, { status: 'FAILURE' }, { generation: { cost: 19 } }), null);
assert.equal(plugin.parseTaskResult({}, { generation: { status: 'done', resultUrl: null } }).status, 'IN_PROGRESS');
assert.equal(plugin.parseTaskResult({}, { generation: {} }).status, 'UNKNOWN');
for (const example of plugin.meta.usageExamples) assert.deepEqual(Object.keys(example.facts).sort(), Object.keys(plugin.meta.usageSchema).sort());
console.log('Passed 97 upstream price tiers and reference, billing, validation and pending-media regressions');
