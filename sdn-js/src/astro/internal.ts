export interface TleLines {
  line1: string;
  line2: string;
  name?: string;
}

export function splitTleForPropagation(tle: string | TleLines): TleLines {
  if (typeof tle !== 'string') {
    return tle;
  }
  const lines = tle
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line.length > 0);
  const line1 = lines.find((line) => line.startsWith('1 '));
  const line2 = lines.find((line) => line.startsWith('2 '));
  if (!line1 || !line2) {
    throw new Error('TLE input must contain element lines starting with "1 " and "2 "');
  }
  return { line1, line2 };
}
