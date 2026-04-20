import { describe, expect, it } from 'vitest';

import { setActiveFeatureSlide } from '../../ui/src/controllers/feature-carousel-controller';

describe('setActiveFeatureSlide', () => {
  it('marks the selected slide and indicator active', () => {
    const root = new FakeRoot([
      new FakeElement('data-feature-slide', 'marketplace', ['sdn-feature-slide']),
      new FakeElement('data-feature-slide', 'directory', ['sdn-feature-slide']),
      new FakeElement('data-feature-target', 'marketplace'),
      new FakeElement('data-feature-target', 'directory'),
    ]);

    setActiveFeatureSlide(root as unknown as HTMLElement, 'directory');

    expect(
      root
        .querySelector('[data-feature-slide="directory"]')
        ?.classList.contains('sdn-feature-slide--active'),
    ).toBe(true);
    expect(
      root
        .querySelector('[data-feature-target="directory"]')
        ?.classList.contains('sdn-feature-carousel__indicator--active'),
    ).toBe(true);
  });
});

class FakeRoot {
  constructor(private readonly elements: FakeElement[]) {}

  querySelector(selector: string): FakeElement | null {
    return this.querySelectorAll(selector)[0] ?? null;
  }

  querySelectorAll(selector: string): FakeElement[] {
    const match = selector.match(/^\[(data-[a-z-]+)(?:="([^"]+)")?\]$/);
    if (!match) {
      return [];
    }
    const [, attribute, value] = match;
    return this.elements.filter((element) => {
      if (!element.hasAttribute(attribute)) {
        return false;
      }
      return value === undefined || element.getAttribute(attribute) === value;
    });
  }
}

class FakeElement {
  readonly classList: FakeClassList;
  private readonly attributes = new Map<string, string>();

  constructor(attribute: string, value: string, initialClasses: string[] = []) {
    this.attributes.set(attribute, value);
    this.classList = new FakeClassList(initialClasses);
  }

  getAttribute(name: string): string | null {
    return this.attributes.get(name) ?? null;
  }

  hasAttribute(name: string): boolean {
    return this.attributes.has(name);
  }

  setAttribute(name: string, value: string): void {
    this.attributes.set(name, value);
  }
}

class FakeClassList {
  private readonly values = new Set<string>();

  constructor(initialValues: string[]) {
    for (const value of initialValues) {
      this.values.add(value);
    }
  }

  contains(name: string): boolean {
    return this.values.has(name);
  }

  toggle(name: string, force?: boolean): void {
    if (force ?? !this.values.has(name)) {
      this.values.add(name);
      return;
    }
    this.values.delete(name);
  }
}
