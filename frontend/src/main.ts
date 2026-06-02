import './app.css';
import { mount } from 'svelte';
import App from './lib/components/App.svelte';

mount(App, {
    target: document.getElementById('app')!,
});