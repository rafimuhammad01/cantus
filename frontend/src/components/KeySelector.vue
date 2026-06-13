<script setup lang="ts">
import { computed } from "vue";
import { semitonesToLabel } from "@/utils/labels";

const props = defineProps<{
  semitones: number;
  disabled?: boolean;
  originalKey?: string;
  transposedKey?: string;
  compact?: boolean;
}>();
const emit = defineEmits<{ change: [value: number] }>();

const MIN = -12;
const MAX = 12;

const primaryLabel = computed(() => semitonesToLabel(props.semitones));

const secondaryLabel = computed(() => {
  if (!props.originalKey) return "";
  if (props.semitones === 0) return `Key: ${props.originalKey}`;
  if (!props.transposedKey) return props.originalKey;
  return `${props.originalKey} → ${props.transposedKey}`;
});

const canDec = computed(() => !props.disabled && props.semitones > MIN);
const canInc = computed(() => !props.disabled && props.semitones < MAX);

function dec() {
  if (canDec.value) emit("change", props.semitones - 1);
}
function inc() {
  if (canInc.value) emit("change", props.semitones + 1);
}
</script>

<template>
  <div class="flex flex-col items-center gap-1.5">
    <div
      v-if="!props.compact"
      class="text-[11px] uppercase tracking-[0.16em] text-[var(--color-text-faint)]"
    >
      Key
    </div>
    <div
      class="inline-flex items-center rounded-full bg-[var(--color-surface)] border border-[var(--color-border)] p-1"
    >
      <button
        @click="dec"
        :disabled="!canDec"
        class="w-8 h-8 rounded-full flex items-center justify-center text-[var(--color-text)] text-base hover:bg-[var(--color-surface-2)] disabled:text-[var(--color-text-faint)] disabled:hover:bg-transparent disabled:cursor-not-allowed transition-colors"
        aria-label="Lower one step"
      >
        −
      </button>
      <div
        class="min-w-[9rem] px-2 text-center text-[13px] font-medium text-[var(--color-text)] select-none"
      >
        {{ primaryLabel }}
      </div>
      <button
        @click="inc"
        :disabled="!canInc"
        class="w-8 h-8 rounded-full flex items-center justify-center text-[var(--color-text)] text-base hover:bg-[var(--color-surface-2)] disabled:text-[var(--color-text-faint)] disabled:hover:bg-transparent disabled:cursor-not-allowed transition-colors"
        aria-label="Higher one step"
      >
        +
      </button>
    </div>
    <div
      v-if="secondaryLabel && !props.compact"
      class="text-[11px] uppercase tracking-[0.16em] text-[var(--color-text-faint)] tnum"
    >
      {{ secondaryLabel }}
    </div>
  </div>
</template>
