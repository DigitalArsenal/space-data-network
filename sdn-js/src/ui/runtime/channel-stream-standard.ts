import { parseChannelId } from '../../channels';

export function assertChannelStreamMatchesStandardCode(channelId: string, streamBytes: Uint8Array): string {
  const { standardCode } = parseChannelId(channelId);
  const view = new DataView(streamBytes.buffer, streamBytes.byteOffset, streamBytes.byteLength);
  let offset = 0;
  let index = 0;
  while (offset < streamBytes.byteLength) {
    if (streamBytes.byteLength - offset < 8) {
      throw new Error(`truncated channel stream frame header at offset ${offset}`);
    }
    const length = view.getUint32(offset, true);
    const frameStart = offset + 4;
    const frameEnd = frameStart + length;
    if (length < 8 || frameEnd > streamBytes.byteLength) {
      throw new Error(`truncated channel stream frame at index ${index}`);
    }
    const fileIdentifier = new TextDecoder().decode(streamBytes.subarray(frameStart + 4, frameStart + 8));
    const frameStandardCode = standardCodeFromFileIdentifier(fileIdentifier);
    if (frameStandardCode !== standardCode) {
      throw new Error(
        `channel stream frame file identifier ${JSON.stringify(fileIdentifier)} does not match channel standardCode ${standardCode}`,
      );
    }
    offset = frameEnd;
    index += 1;
  }
  return standardCode;
}

function standardCodeFromFileIdentifier(fileIdentifier: string): string {
  const trimmed = fileIdentifier.trim();
  if (trimmed.length === 4 && trimmed.startsWith('$')) {
    return trimmed.slice(1);
  }
  return trimmed.slice(0, 3);
}
