<script setup lang="ts">
import { computed } from "vue";

const props = defineProps<{
  current: -12 | 0 | 12;
  disabled?: boolean;
  range?: string; // e.g. "C3 – G4"; rendered as italic Fraunces secondary line
}>();

const emit = defineEmits<{
  change: [shift: -12 | 0 | 12];
}>();

interface Choice {
  value: -12 | 0 | 12;
  label: "Low" | "Mid" | "High";
}
const choices: Choice[] = [
  { value: -12, label: "Low" },
  { value: 0, label: "Mid" },
  { value: 12, label: "High" },
];

function select(value: -12 | 0 | 12) {
  if (!props.disabled) emit("change", value);
}

const showRange = computed(() => !!props.range);
</script>

<template>
  <div class="flex flex-col items-center gap-1.5">
    <div
      class="text-[11px] uppercase tracking-[0.16em] text-[var(--color-text-faint)]"
    >
      Voice
    </div>
    <div
      class="inline-flex items-center rounded-full bg-[var(--color-surface)] border border-[var(--color-border)] p-1"
    >
      <button
        v-for="c in choices"
        :key="c.value"
        @click="select(c.value)"
        :disabled="props.disabled"
        :aria-pressed="props.current === c.value"
        class="px-4 py-1.5 rounded-full text-[13px] font-medium transition-colors"
        :class="[
          props.disabled
            ? 'text-[var(--color-text-faint)] cursor-not-allowed'
            : props.current === c.value
              ? 'bg-[var(--color-accent)] text-[#0a0a0b]'
              : 'text-[var(--color-text)] hover:bg-[var(--color-surface-2)]',
        ]"
      >
        {{ c.label }}
      </button>
    </div>
    <div
      v-if="showRange"
      class="serif-italic text-[13px] text-[var(--color-text-muted)]"
    >
      {{ range }}
    </div>
  </div>
</template>
