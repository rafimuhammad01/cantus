<script setup lang="ts">
const props = defineProps<{
  current: -12 | 0 | 12;
  disabled?: boolean;
}>();

const emit = defineEmits<{
  change: [shift: -12 | 0 | 12];
}>();

function select(shift: -12 | 0 | 12): void {
  if (!props.disabled) emit("change", shift);
}
</script>

<template>
  <div
    class="inline-flex items-center rounded-full bg-[#1a1822] border border-[#2a2730] overflow-hidden"
  >
    <button
      @click="select(-12)"
      :disabled="props.disabled"
      class="px-4 py-2 text-sm font-medium text-white transition-colors"
      :class="
        props.disabled
          ? 'text-gray-600 cursor-not-allowed'
          : props.current === -12
            ? 'bg-[#2ca02c]'
            : 'hover:bg-[#23202c]'
      "
      aria-label="Shift target down one octave"
    >
      ↓ Octave
    </button>
    <button
      @click="select(0)"
      :disabled="props.disabled"
      class="px-4 py-2 text-sm font-medium text-white border-l border-r border-[#2a2730] transition-colors"
      :class="
        props.disabled
          ? 'text-gray-600 cursor-not-allowed'
          : props.current === 0
            ? 'bg-[#2ca02c]'
            : 'hover:bg-[#23202c]'
      "
      aria-label="Use original octave for target"
    >
      Original
    </button>
    <button
      @click="select(12)"
      :disabled="props.disabled"
      class="px-4 py-2 text-sm font-medium text-white transition-colors"
      :class="
        props.disabled
          ? 'text-gray-600 cursor-not-allowed'
          : props.current === 12
            ? 'bg-[#2ca02c]'
            : 'hover:bg-[#23202c]'
      "
      aria-label="Shift target up one octave"
    >
      ↑ Octave
    </button>
  </div>
</template>
