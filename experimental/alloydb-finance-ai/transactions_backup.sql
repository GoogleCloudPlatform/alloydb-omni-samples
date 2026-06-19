--
-- PostgreSQL database dump
--

\restrict FRkfx1vGNV4SVvmMXV0eFWcwkrDTfmp3U7qkHVaunLjRW8eai3hXL6AKxqLHjaE

-- Dumped from database version 18.3 (Homebrew)
-- Dumped by pg_dump version 18.3 (Homebrew)

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Data for Name: transactions; Type: TABLE DATA; Schema: public; Owner: sriyasth
--

COPY public.transactions (transaction_id, user_id, "timestamp", amount, merchant_name, merchant_category, spending_category, transaction_type, payment_method, city, country, currency, description) FROM stdin;
9990bb59-e66b-4a49-b184-48146034e95f	1	2026-04-16 20:11:52.830724-07	-50.00	Trader Joe's	supermarket	groceries	debit	debit_card	Berkeley	US	USD	\N
\.


--
-- PostgreSQL database dump complete
--

\unrestrict FRkfx1vGNV4SVvmMXV0eFWcwkrDTfmp3U7qkHVaunLjRW8eai3hXL6AKxqLHjaE

