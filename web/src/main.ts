import { mount } from 'svelte';
import App from './App.svelte';
import { clock } from './lib/stores/clock.svelte';
import './app.css';

const target = document.getElementById('app');
if (!target) throw new Error('#app missing from index.html');

clock.start();

export default mount(App, { target });
